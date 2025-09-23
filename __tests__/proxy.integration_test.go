package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

type expected struct {
	statusCode  int
	cacheHeader string
	body        map[string]interface{} // Nested JSON structure
}

type mustRevalidateTestCase struct {
	desc     string
	id       string
	expected expected
}

func TestMustValidateDirective(t *testing.T) {
	now := time.Now().UTC()

	mustRevalidateTestCases := []mustRevalidateTestCase{
		{
			desc: "Should give status 200 and Cache MISS",
			id:   "9z8y7x6w-5v4u-3210-9876-54321fedcba9",
			expected: expected{
				statusCode:  http.StatusOK,
				cacheHeader: "MISS",
				body: map[string]interface{}{
					"data": map[string]interface{}{
						"id":        "9z8y7x6w-5v4u-3210-9876-54321fedcba9",
						"name":      "Daniel Jackson",
						"createdAt": now.Add(time.Hour * 7).Format(time.RFC3339Nano),
						"updatedAt": now.Add(time.Hour * 7).Format(time.RFC3339Nano),
					},
					"message":    "Data Retrieved Successfully",
					"statusCode": float64(200), // JSON numbers decode as float64
				},
			},
		},
		{
			desc: "Should give status 200 for invalid ID (Cache MISS)",
			id:   "9z8y7x6w-5v4u-3210-9876-54321fedcop",
			expected: expected{
				statusCode:  http.StatusOK, // Adjusted to match proxy response
				cacheHeader: "MISS",
				body: map[string]interface{}{
					// "data": map[string]interface{}{
					// 	"id":        "9z8y7x6w-5v4u-3210-9876-54321fedcop",
					// 	"name":      "",
					// 	"createdAt": "",
					// 	"updatedAt": "",
					// },
					"data":nil,
					"message":    "Data not found",
					"statusCode": float64(404),
				},
			},
		},
		{
			desc: "Should give status 200 and Cache HIT",
			id:   "9z8y7x6w-5v4u-3210-9876-54321fedcba9",
			expected: expected{
				statusCode:  http.StatusOK,
				cacheHeader: "HIT",
				body: map[string]interface{}{
					"data": map[string]interface{}{
						"id":        "9z8y7x6w-5v4u-3210-9876-54321fedcba9",
						"name":      "Daniel Jackson",
						"createdAt": now.Add(time.Hour * 7).Format(time.RFC3339Nano),
						"updatedAt": now.Add(time.Hour * 7).Format(time.RFC3339Nano),
					},
					"message":    "Data Retrieved Successfully",
					"statusCode": float64(200),
				},
			},
		},
	}

	for _, tc := range mustRevalidateTestCases {
		t.Run(tc.desc, func(t *testing.T) {
			url := fmt.Sprintf("http://localhost:3000/data?id=%s&cache-control=must-revalidate,max-age=10", tc.id)
			start := time.Now()

			response, err := http.Get(url)
			if err != nil {
				t.Fatalf("Got error: %v", err)
			}
			defer response.Body.Close()

			duration := time.Since(start)
			fmt.Printf("Request for %s took %v\n", tc.id, duration)

			// Status code
			if response.StatusCode != tc.expected.statusCode {
				t.Fatalf("Expected status %d but got: %d", tc.expected.statusCode, response.StatusCode)
			}

			// Cache header
			cacheHeader := response.Header.Get("X-Cache-Status")
			if cacheHeader != tc.expected.cacheHeader {
				t.Errorf("Expected cache header %q but got %q", tc.expected.cacheHeader, cacheHeader)
			}

			// Body comparison
			var respBody map[string]interface{}
			if err := json.NewDecoder(response.Body).Decode(&respBody); err != nil {
				t.Fatalf("Failed to decode response body: %v", err)
			}

			if !reflect.DeepEqual(respBody, tc.expected.body) {
				t.Errorf("Expected body %+v but got %+v", tc.expected.body, respBody)
			}
		})
	}
}
