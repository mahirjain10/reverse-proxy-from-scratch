package main

import (
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mahirjain10/reverse-proxy/constant"
)

type Data struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ResponseData struct {
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
	Data       *Data  `json:"data"`
}

func initData() []Data {
	now := time.Now()
	return []Data{
		{
			ID:        "c1b2d3e4-f5a6-7890-1234-567890123456",
			Name:      "Charlie Brown",
			CreatedAt: now.Add(time.Hour * 1),
			UpdatedAt: now.Add(time.Hour * 1),
		},
		{
			ID:        "g8h7i6j5-k4l3-2109-8765-43210fedcba9",
			Name:      "Dana Scully",
			CreatedAt: now.Add(time.Hour * 2),
			UpdatedAt: now.Add(time.Hour * 2),
		},
		{
			ID:        "i9j8k7l6-m5n4-3210-9876-54321fedcba0",
			Name:      "Fox Mulder",
			CreatedAt: now.Add(time.Hour * 3),
			UpdatedAt: now.Add(time.Hour * 3),
		},
		{
			ID:        "p1q2r3s4-t5u6-7890-1234-567890abcdef",
			Name:      "Jack O'Neill",
			CreatedAt: now.Add(time.Hour * 4),
			UpdatedAt: now.Add(time.Hour * 4),
		},
		{
			ID:        "x9y8z7a6-b5c4-3210-9876-54321fedcba9",
			Name:      "Samantha Carter",
			CreatedAt: now.Add(time.Hour * 5),
			UpdatedAt: now.Add(time.Hour * 5),
		},
		{
			ID:        "1a2b3c4d-5e6f-7890-1234-567890123456",
			Name:      "Teal'c",
			CreatedAt: now.Add(time.Hour * 6),
			UpdatedAt: now.Add(time.Hour * 6),
		},
		{
			ID:        "9z8y7x6w-5v4u-3210-9876-54321fedcba9",
			Name:      "Daniel Jackson",
			CreatedAt: now.Add(time.Hour * 7),
			UpdatedAt: now.Add(time.Hour * 7),
		},
	}
}


func writeJSONResponse(w http.ResponseWriter, statusCode int, payload any) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "Error marshaling JSON", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if _, err := w.Write(jsonData); err != nil {
		log.Println("Error writing response:", err)
	}
}

func generateETag(t time.Time) string {
	b := t.AppendFormat(nil, time.RFC3339Nano)
	hash := md5.Sum(b)
	return fmt.Sprintf(`"%x"`, hash)
}

func handlerGetMethod(w http.ResponseWriter, r *http.Request, data []Data) {
	id := r.URL.Query().Get("id")

	var fetchedResult *Data
	for i := range data {
		if data[i].ID == id {
			fetchedResult = &data[i]
			break
		}
	}

	if fetchedResult == nil {
		response := ResponseData{
			Message:    "Data not found",
			StatusCode: http.StatusNotFound,
			Data:       nil,
		}
		writeJSONResponse(w, http.StatusNotFound, response)
		return
	}

	etag := generateETag(fetchedResult.UpdatedAt)
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	sendCacheControl := r.URL.Query().Get("cache-control")
	if (strings.Contains(sendCacheControl, constant.MUST_REVALIDATE) && strings.Contains(sendCacheControl, constant.MAX_AGE)) ||
		(strings.Contains(sendCacheControl, constant.STALE_WHILE_REVALIDATE) && strings.Contains(sendCacheControl, constant.MAX_AGE)) ||
		(strings.Contains(sendCacheControl, constant.STALE_IF_ERROR) && strings.Contains(sendCacheControl, constant.MAX_AGE)) {
		w.Header().Set("Cache-Control", sendCacheControl)
	}

	w.Header().Set("ETag", etag)
	response := ResponseData{
		Message:    "Data Retrieved Successfully",
		StatusCode: http.StatusOK,
		Data:       fetchedResult,
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func handlePutMethod(w http.ResponseWriter, r *http.Request, data []Data) {
	id := r.URL.Query().Get("id")

	var fetchedResult *Data
	for i := range data {
		if data[i].ID == id {
			data[i].UpdatedAt = time.Now()
			fetchedResult = &data[i]
			break
		}
	}

	if fetchedResult == nil {
		response := ResponseData{
			Message:    "Data not found, Didn't update Data",
			StatusCode: http.StatusNotFound,
			Data:       nil,
		}
		writeJSONResponse(w, http.StatusNotFound, response)
		return
	}

	if sendCacheControl := r.URL.Query().Get("cache-control"); sendCacheControl == "true" {
		w.Header().Set("Cache-Control", "must-revalidate, max-age=3600")
	}

	response := ResponseData{
		Message:    "Data Updated Successfully",
		StatusCode: http.StatusOK,
		Data:       fetchedResult,
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func main() {
	port := flag.String("port", ":8000", "Port to run the server on")
	flag.Parse()

	data := initData()

	http.HandleFunc("/health-check", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("healthy"))
	})

	http.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handlerGetMethod(w, r, data)
		case http.MethodPut:
			handlePutMethod(w, r, data)
		default:
			http.Error(w, "Only GET and PUT methods are allowed", http.StatusMethodNotAllowed)
		}
	})

	fmt.Println("Mock server started on port", *port)
	if err := http.ListenAndServe(*port, nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}