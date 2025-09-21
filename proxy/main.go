package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mahirjain10/reverse-proxy/constant"
	"github.com/redis/go-redis/v9"
)

// --- Structs for Data Management ---

const IN_PROCESS = "IN_PROCESS"
const SUCCESS = "SUCCESS"
const FAILED = "FAILED"

type Status struct {
	status string
	mu     sync.Mutex
}
type CacheControl struct {
	cacheType      string
	maxAge         int // in seconds
	serveStaleUpto int // in seconds
}

type Data struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type Cache struct {
	Body         string            `json:"body"`
	Headers      map[string]string `json:"headers"`
	StoredAt     int64             `json:"stored_at"`
	MaxAge       int               `json:"max_age"`
	UseStaleUpto int               `json:"use_stale_upto"`
	IsCacheStale bool              `json:"is_cache_stale"`
}

// --- Redis Client and Cache Management Functions ---

func initializeRedisClient(redisURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("encountered error while initializing redis client: %w", err)
	}
	return redis.NewClient(opt), nil
}

func getCacheKey(origin string, r *http.Request) string {
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	query := r.URL.RawQuery
	return "cache:" + r.Method + ":" + origin + path + "?" + query
}

func (cache *CacheControl) getDataFromHeaderString(headerValue string) {
	parts := strings.Split(headerValue, ",")
	if len(parts) < 2 {
		fmt.Println("invalid cache-control header format")
		return
	}

	cacheType := strings.TrimSpace(parts[0])
	if strings.Contains(cacheType, constant.MUST_REVALIDATE) {
		cache.cacheType = cacheType
	} else if strings.Contains(cacheType, constant.STALE_WHILE_REVALIDATE) || strings.Contains(cacheType, constant.STALE_IF_ERROR) {
		valueParts := strings.Split(cacheType, "=")
		if len(valueParts) == 2 {
			useStaleUpto, err := strconv.Atoi(valueParts[1])
			if err == nil {
				cache.serveStaleUpto = useStaleUpto
			}
		}
	}

	maxAgePart := strings.TrimSpace(parts[1])
	valueParts := strings.Split(maxAgePart, "=")
	if len(valueParts) == 2 {
		maxAge, err := strconv.Atoi(valueParts[1])
		if err == nil {
			cache.maxAge = maxAge
		}
	}
}

func fetchFromOrigin(origin string, etag string, r *http.Request) (string, int, map[string]string, error) {
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	query := r.URL.RawQuery

	url := origin + path
	if query != "" {
		url += "?" + query
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", http.StatusInternalServerError, nil, fmt.Errorf("error creating request: %w", err)
	}

	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", http.StatusBadGateway, nil, fmt.Errorf("error forwarding request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return "", resp.StatusCode, nil, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", http.StatusInternalServerError, nil, fmt.Errorf("error reading response body: %w", err)
	}

	headers := make(map[string]string)
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, ", ")
	}

	return string(body), resp.StatusCode, headers, nil
}

// --- Reusable Cache Revalidation Function ---

func revalidateAndUpdateCache(ctx context.Context, redisClient *redis.Client, key string, cachedData *Cache, origin, portToUse string, r *http.Request) error {
	fullOrigin := origin + portToUse
	etag := cachedData.Headers["Etag"]
	newBody, statusCode, newHeaders, err := fetchFromOrigin(fullOrigin, etag, r)
	if err != nil {
		return fmt.Errorf("failed to revalidate from origin: %w", err)
	}

	fmt.Println("Revalidation response status code:", statusCode)

	if statusCode == http.StatusNotModified {
		fmt.Println("Origin returned 304 Not Modified. Renewing cache.")
		cachedData.StoredAt = time.Now().Unix()
	} else if statusCode == http.StatusOK {
		fmt.Println("Origin returned 200 OK. Updating cache with new data.")
		cachedData.Body = newBody
		cachedData.Headers = newHeaders
		cachedData.StoredAt = time.Now().Unix()
	}

	updatedCacheJSON, marshalErr := json.Marshal(cachedData)
	if marshalErr != nil {
		return fmt.Errorf("error marshaling updated cache data: %w", marshalErr)
	}

	return redisClient.Set(ctx, key, updatedCacheJSON, 0).Err()
}

// --- HTTP Handlers ---

func handleProxy(redisClient *redis.Client, origin string, healthCheckMap map[string]bool, status *Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var responseStatusCode int = http.StatusOK
		var didCacheHit = true
		var responseBody string
		var responseHeaders map[string]string

		key := getCacheKey(origin, r)
		fmt.Println("Cache key:", key)

		value, err := redisClient.Get(ctx, key).Result()
		if err == redis.Nil {
			// --- CACHE MISS ---
			didCacheHit = false
			fmt.Println("Cache miss, fetching from origin...")

			portToUse := ""
			for p, isHealthy := range healthCheckMap {
				if isHealthy {
					portToUse = p
					break
				}
			}
			if portToUse == "" {
				http.Error(w, "No healthy origin server available", http.StatusBadGateway)
				return
			}
			fullOrigin := origin + portToUse

			body, statusCode, headers, fetchErr := fetchFromOrigin(fullOrigin, "", r)
			if fetchErr != nil {
				http.Error(w, fetchErr.Error(), http.StatusBadGateway)
				return
			}

			responseBody = body
			responseStatusCode = statusCode
			responseHeaders = headers

			var cacheControlStruct CacheControl
			if ccHeader, ok := headers["Cache-Control"]; ok {
				cacheControlStruct.getDataFromHeaderString(ccHeader)
			}

			cacheData := Cache{
				Body:         responseBody,
				Headers:      responseHeaders,
				StoredAt:     time.Now().Unix(),
				MaxAge:       cacheControlStruct.maxAge,
				UseStaleUpto: cacheControlStruct.serveStaleUpto,
			}

			cacheJSON, marshalErr := json.Marshal(cacheData)
			if marshalErr != nil {
				log.Printf("Error marshaling cache data: %v", marshalErr)
			} else {
				redisClient.Set(ctx, key, cacheJSON, 0)
			}
		} else if err != nil {
			http.Error(w, "cache error: "+err.Error(), http.StatusInternalServerError)
			return
		} else {
			// --- CACHE HIT ---
			fmt.Println("Cache hit")
			var cachedData Cache
			if err := json.Unmarshal([]byte(value), &cachedData); err != nil {
				http.Error(w, "error unmarshaling cache data", http.StatusInternalServerError)
				return
			}

			isExpired := time.Now().Unix() > cachedData.StoredAt+int64(cachedData.MaxAge)
			staleWindowEnd := cachedData.StoredAt + int64(cachedData.MaxAge) + int64(cachedData.UseStaleUpto)
			isStalePeriodOver := time.Now().Unix() > staleWindowEnd

			needsRevalidation := strings.Contains(cachedData.Headers["Cache-Control"], constant.MUST_REVALIDATE)
			serveStaleWhileRevalidate := strings.Contains(cachedData.Headers["Cache-Control"], constant.STALE_WHILE_REVALIDATE)

			portToUse := ""
			for p, isHealthy := range healthCheckMap {
				if isHealthy {
					portToUse = p
					break
				}
			}
			if (isExpired || isStalePeriodOver) && status.status == SUCCESS {
				status.mu.Lock()
				status.status = FAILED
				fmt.Println("MARKING STATUS FROM SUCCESS TO FAILED")
				status.mu.Unlock()
			}
			if portToUse != "" {
				fmt.Println("THE STATUS IS : ", status.status)
				if needsRevalidation && isExpired && status.status == FAILED || serveStaleWhileRevalidate && isExpired && isStalePeriodOver && status.status == FAILED {
					status.mu.Lock()
					status.status = IN_PROCESS
					defer status.mu.Unlock()
					time.Sleep(20 * time.Second)
					fmt.Println("Cache expired, attempting revalidation...")
					// lockKey := "lock:" + key
					// lock, err := locker.Obtain(ctx, lockKey, 10*time.Second, nil)
					// if err != nil {
					// 	log.Printf("Could not obtain lock for %s. Serving stale.", key)
					// 	w.Header().Set("X-Cache-Status", "STALE")
					// } else {
					// defer lock.Release(ctx)
					fmt.Println("Lock obtained, revalidating with origin.")
					if err := revalidateAndUpdateCache(ctx, redisClient, key, &cachedData, origin, portToUse, r); err != nil {
						log.Printf("Error during cache revalidation: %v", err)
						status.status = FAILED
					}
					status.status = SUCCESS
					// }
				}
				if serveStaleWhileRevalidate && isExpired && !isStalePeriodOver && status.status == FAILED {
					go func() {
						status.mu.Lock()
						status.status = IN_PROCESS
						defer status.mu.Unlock()

						newCtx := context.Background()
						fmt.Println("Calling Origin async")
						time.Sleep(20 * time.Second)
						// lockKey := "lock:" + key
						// lock, err := locker.Obtain(newCtx, lockKey, 10*time.Second, nil)
						// if err != nil {
						// 	log.Printf("Could not obtain lock for async refresh %s", key)
						// 	fmt.Println("Error while obtaining lock ",err)
						// 	return
						// }
						// defer lock.Release(newCtx)
						if err := revalidateAndUpdateCache(newCtx, redisClient, key, &cachedData, origin, portToUse, r); err != nil {
							log.Printf("Error during async cache refresh: %v", err)
							status.status = FAILED

						}
						status.status = SUCCESS

					}()
				}
			}
			if status.status == FAILED || status.status == IN_PROCESS {
				fmt.Println("SERVING STALE")
			} else if status.status == SUCCESS {
				fmt.Println("SERVING FRESH")
			}
			if needsRevalidation && status.status == SUCCESS || serveStaleWhileRevalidate && (status.status == IN_PROCESS || status.status == FAILED || status.status == SUCCESS) {
				responseBody = cachedData.Body
				responseHeaders = cachedData.Headers
				responseStatusCode = http.StatusOK
				// --- Write Response to Client ---
				for k, v := range responseHeaders {
					w.Header().Set(k, v)
				}
				w.Header().Set("X-Cache-Status", map[bool]string{true: "HIT", false: "MISS"}[didCacheHit])
				w.WriteHeader(responseStatusCode)
				w.Write([]byte(responseBody))
			}
		}

	}
}

func CheckHealth(ports []string, origin string, healthCheckMap map[string]bool) {
	for _, port := range ports {
		fullOrigin := origin + port
		resp, err := http.Get(fullOrigin + "/health-check")
		if err != nil || resp.StatusCode != http.StatusOK {
			healthCheckMap[port] = false
		} else {
			healthCheckMap[port] = true
			resp.Body.Close()
		}
	}
}

// --- Main Function ---

func main() {
	originPorts := flag.String("ports", "8000", "Comma-separated list of origin server ports")
	origin := flag.String("origin", "http://localhost:", "Default Origin server URL")
	flag.Parse()

	if *origin == "" {
		log.Fatal("origin required")
	}

	ports := strings.Split(*originPorts, ",")
	healthCheckMap := make(map[string]bool)
	var status Status
	CheckHealth(ports, *origin, healthCheckMap)
	fmt.Printf("Initial health check results: %v\n", healthCheckMap)
	status.status = FAILED
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			CheckHealth(ports, *origin, healthCheckMap)
			fmt.Printf("Health check update: %v\n", healthCheckMap)
		}
	}()

	redisClient, err := initializeRedisClient("redis://localhost:6379/0")
	if err != nil {
		log.Fatal(err)
	}
	// locker := redislock.New(redisClient)

	http.HandleFunc("/", handleProxy(redisClient, *origin, healthCheckMap, &status))

	fmt.Println("Proxy server started successfully at :3000")
	if err := http.ListenAndServe(":3000", nil); err != nil {
		log.Fatalf("Error starting the proxy server: %v", err)
	}
}
