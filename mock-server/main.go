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

func handlerGetMethod(w http.ResponseWriter, r *http.Request, data []Data) {
	id := r.URL.Query().Get("id")
	sendCacheControl := r.URL.Query().Get("cache-control")
	RequestETag := r.Header.Get("If-None-Match")
	fmt.Println(r.URL.RawQuery)
	var fetchedResult *Data
	message := "Data not found"
	statusCode := http.StatusNotFound
	fmt.Println("Printing Request Etag",RequestETag)
	fmt.Println("Printing Request Etag len",len(RequestETag))
	if len(RequestETag) != 0 {
		fmt.Println("IN IF")
		for i := range data {
			if data[i].ID == id {
				fetchedResult = &data[i]
				message = "Data Retrieved Successfully"
				statusCode = http.StatusOK
				break
			}
		}
		t := fetchedResult.UpdatedAt
		b := t.AppendFormat(nil, time.RFC3339Nano) // converts time to []byte
		etag := md5.Sum(b)
		etagStr := fmt.Sprintf(`"%x"`, etag)
		fmt.Printf("Printing ETag String IN IF %x\n", etagStr)
		if etagStr == RequestETag {
			fmt.Println("HELL YEAH")
			w.Header().Set("ETag", etagStr)
			w.WriteHeader(304)
			return
			// w.Write([]byte("Nothing Changed"))
		}
	}

	for i := range data {
		if data[i].ID == id {
			fetchedResult = &data[i]
			message = "Data Retrieved Successfully"
			statusCode = http.StatusOK
			break
		}
	}

	responseData := ResponseData{
		Message:    message,
		StatusCode: statusCode,
		Data:       fetchedResult,
	}

	jsonData, err := json.Marshal(responseData)
	if err != nil {
		http.Error(w, "Error marshaling JSON", http.StatusInternalServerError)
		return
	}

	if (strings.Contains(sendCacheControl, constant.MUST_REVALIDATE) && strings.Contains(sendCacheControl, constant.MAX_AGE)) ||
		(strings.Contains(sendCacheControl, constant.STALE_WHILE_REVALIDATE) && strings.Contains(sendCacheControl, constant.MAX_AGE)) ||
		(strings.Contains(sendCacheControl, constant.STALE_IF_ERROR) && strings.Contains(sendCacheControl, constant.MAX_AGE)) {
		fmt.Println("IN CACHE CONTROL IF")
		w.Header().Set("Cache-Control", sendCacheControl)
	}

	t := fetchedResult.UpdatedAt
	b := t.AppendFormat(nil, time.RFC3339Nano) // converts time to []byte
	etag := md5.Sum(b)
	etagStr := fmt.Sprintf(`"%x"`, etag)
	fmt.Printf("Printing Etag post string conv %x\n", etagStr)
	w.Header().Set("ETag", etagStr)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	_, err = w.Write(jsonData)
	if err != nil {
		log.Println("Error writing response:", err)
	}
}

func handlePutMethod(w http.ResponseWriter, r *http.Request, data []Data) {
	id := r.URL.Query().Get("id")
	sendCacheControl := r.URL.Query().Get("cache-control")

	var fetchedResult *Data
	message := "Data not found, Didn't update Data"
	statusCode := http.StatusNotFound

	for i := range data {
		if data[i].ID == id {
			data[i].UpdatedAt = time.Now()
			fetchedResult = &data[i]
			message = "Data Updated Successfully"
			statusCode = http.StatusOK
			break
		}
	}

	responseData := ResponseData{
		Message:    message,
		StatusCode: statusCode,
		Data:       fetchedResult,
	}

	jsonData, err := json.Marshal(responseData)
	if err != nil {
		http.Error(w, "Error marshaling JSON", http.StatusInternalServerError)
		return
	}

	if sendCacheControl == "true" {
		w.Header().Set("Cache-Control", "must-revalidate, max-age=3600")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	_, err = w.Write(jsonData)
	if err != nil {
		log.Println("Error writing response:", err)
	}
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
			http.Error(w, "Only GET and PUT methods allowed", http.StatusMethodNotAllowed)
		}
	})

	fmt.Println("Mock server started on port", *port)
	err := http.ListenAndServe(*port, nil)
	if err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
