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

type CacheControl struct {
	cacheType      string
	maxAge         int // in seconds
	serveStaleUpto int // in seconds
}

type Cache struct {
	CachType     string            `json:"cache_type"`
	Body         string            `json:"body"`
	Headers      map[string]string `json:"headers"`
	StoredAt     int64             `json:"stored_at"`
	MaxAge       int               `json:"max_age"`
	UseStaleUpto int               `json:"use_stale_upto"`
}

type CacheManager struct {
	redisClient *redis.Client
	leaderMap   sync.Map
}

func NewCacheManager(redisClient *redis.Client) *CacheManager {
	return &CacheManager{
		redisClient: redisClient,
	}
}

type Proxy struct {
	origin         string
	healthCheckMap map[string]bool
	cacheManager   *CacheManager
}

func NewProxy(origin string, healthCheckMap map[string]bool, cm *CacheManager) *Proxy {
	return &Proxy{
		origin:         origin,
		healthCheckMap: healthCheckMap,
		cacheManager:   cm,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var didCacheHit = true
	var cachedData Cache

	key := getCacheKey(p.origin, r)
	fmt.Println("Cache key:", key)

	value, err := p.cacheManager.redisClient.Get(ctx, key).Result()

	// --- CACHE MISS ---
	if err == redis.Nil {
		didCacheHit = false
		fmt.Println("Cache miss, fetching from origin...")
		fetchedData, err, didWeHitCache := p.fetchOnCacheMiss(ctx, key, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		cachedData = *fetchedData
		didCacheHit = didWeHitCache

	} else if err != nil {
		http.Error(w, "cache error: "+err.Error(), http.StatusInternalServerError)
		return

	} else { // --- CACHE HIT ---
		fmt.Println("Cache hit")
		if unmarshalErr := json.Unmarshal([]byte(value), &cachedData); unmarshalErr != nil {
			log.Printf("Failed to unmarshal cache: %v", unmarshalErr)
			http.Error(w, "Failed to unmarshal cache", http.StatusInternalServerError)
			return
		}

		isExpired := time.Now().Unix() > cachedData.StoredAt+int64(cachedData.MaxAge)
		isUseStaleUptoExpired := time.Now().Unix() > cachedData.StoredAt+int64(cachedData.MaxAge)+int64(cachedData.UseStaleUpto)

		fmt.Println("The cache type is : ", cachedData.CachType)
		fmt.Println("Has cache expired : ", isExpired)
		fmt.Println("Has stale expired : ", isUseStaleUptoExpired)

		// If the max age (freshness) expired and STALE_WHILE_REVALIDATE
		// Call the Origin Async
		if isExpired && !isUseStaleUptoExpired && cachedData.CachType == constant.STALE_WHILE_REVALIDATE {
			p.revalidateAsynchronously(key, &cachedData, r)
		}

		// If cache expired && MUST_REVALIDATE
		// If isUseStaleUptoExpired (max age + stale upto) && STALE_IF_ERROR
		// If isUseStaleUptoExpired (max age + stale upto) && STALE_WHILE_REVALIDATE
		// Call the Origin Sync
		if isExpired && cachedData.CachType == constant.MUST_REVALIDATE || isUseStaleUptoExpired && cachedData.CachType == constant.STALE_IF_ERROR || isUseStaleUptoExpired && cachedData.CachType == constant.STALE_WHILE_REVALIDATE {
			fmt.Println("Cache is expired and must be revalidated synchronously.")
			revalidatedData, err := p.revalidateSynchronously(ctx, key, &cachedData, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			cachedData = *revalidatedData
		}
	}

	for k, v := range cachedData.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("X-Cache-Status", map[bool]string{true: "HIT", false: "MISS"}[didCacheHit])
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(cachedData.Body))
}

func (p *Proxy) getHealthyOriginPort() string {
	for port, isHealthy := range p.healthCheckMap {
		if isHealthy {
			return port
		}
	}
	return ""
}

func (p *Proxy) fetchOnCacheMiss(ctx context.Context, key string, r *http.Request) (*Cache, error, bool) {
	myChan := make(chan struct{})
	actualChan, loaded := p.cacheManager.leaderMap.LoadOrStore(key, myChan)
	fmt.Println("Is did we load in cache miss : ", loaded)
	fmt.Println("If no then channel in cache miss : ", actualChan)

	if !loaded { // I am the leader
		fmt.Println("I am the leader for a cache miss.")
		defer func() {
			close(actualChan.(chan struct{}))
			p.cacheManager.leaderMap.Delete(key)
		}()

		// Check for healthy port
		portToUse := p.getHealthyOriginPort()

		// If no healthy port found ,send error
		if portToUse == "" {
			return nil, fmt.Errorf("no healthy origin server available"), false
		}
		fullOrigin := p.origin + portToUse

		time.Sleep(10 * time.Second)
		fmt.Println("10 seconds finished...")
		// Fetch from Origin
		body, _, headers, fetchErr := fetchFromOrigin(ctx, fullOrigin, "", r)
		if fetchErr != nil {
			return nil, fetchErr, false
		}

		// Get the value of cache-control header to parse
		var cacheControlStruct CacheControl
		if ccHeaderValue, ok := headers["Cache-Control"]; ok {
			cacheControlStruct.getCacheControlDataFromHeader(ccHeaderValue)
		}

		// Ready the data to save into redis
		cacheToStore := &Cache{
			CachType:     cacheControlStruct.cacheType,
			Body:         body,
			Headers:      headers,
			StoredAt:     time.Now().Unix(),
			MaxAge:       cacheControlStruct.maxAge,
			UseStaleUpto: cacheControlStruct.serveStaleUpto,
		}

		cacheJSON, marshalErr := json.Marshal(cacheToStore)
		if marshalErr != nil {
			log.Printf("Error marshaling cache data: %v", marshalErr)
		} else {
			fmt.Println("Saving data in Redis for cache MISS : ", loaded)
			p.cacheManager.redisClient.Set(ctx, key, cacheJSON, 0)
		}
		return cacheToStore, nil, true

	} else { // I am a follower
		fmt.Println("Another request is already fetching for this cache miss. I will wait.")
		select {
		case <-actualChan.(chan struct{}):
			fmt.Println("Follower unblocked! Re-fetching from cache after miss.")
			value, err := p.cacheManager.redisClient.Get(ctx, key).Result()
			if err != nil {
				return nil, fmt.Errorf("failed to retrieve cache after leader's fetch"), false
			}
			var cachedData Cache
			if unmarshalErr := json.Unmarshal([]byte(value), &cachedData); unmarshalErr != nil {
				return nil, fmt.Errorf("failed to unmarshal cache for follower"), false
			}
			return &cachedData, nil, false
		case <-ctx.Done():
			return nil, fmt.Errorf("request timed out while waiting for initial cache fill"), false
		}
	}
}

func (p *Proxy) revalidateSynchronously(ctx context.Context, key string, currentData *Cache, r *http.Request) (*Cache, error) {
	portToUse := p.getHealthyOriginPort()
	if portToUse == "" {
		return nil, fmt.Errorf("no healthy origin server available for revalidation")
	}

	myChan := make(chan struct{})
	actualChan, loaded := p.cacheManager.leaderMap.LoadOrStore(key, myChan)

	fmt.Println("Is did we load in cache miss : ", loaded)
	fmt.Println("If no then channel in cache miss : ", actualChan)

	if !loaded { // I am the leader.
		fmt.Println("Cache expired. I am the leader, revalidating...")
		time.Sleep(10 * time.Second) // Simulating work
		if err := revalidateAndUpdateCache(ctx, p.cacheManager.redisClient, key, currentData, p.origin, portToUse, r); err != nil {
			log.Printf("Error during cache revalidation: %v", err)
			return nil, err
		}
		fmt.Println("Closing Channel.")
		close(actualChan.(chan struct{}))
		p.cacheManager.leaderMap.Delete(key)
		fmt.Println("Leader finished revalidation and cleaned up.")
	}
	fmt.Println("Waiting for leader to finish revalidation sync...")
	select {
	case <-actualChan.(chan struct{}):
		fmt.Println("Unblocked! Re-fetching updated item from cache.")
		value, err := p.cacheManager.redisClient.Get(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve refreshed cache")
		}
		var refreshedData Cache
		if err := json.Unmarshal([]byte(value), &refreshedData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal refreshed cache")
		}
		return &refreshedData, nil

	case <-ctx.Done():
		return nil, fmt.Errorf("request timed out while waiting for cache refresh")
	}
}

func (p *Proxy) revalidateAsynchronously(key string, currentData *Cache, r *http.Request) {
	portToUse := p.getHealthyOriginPort()
	if portToUse == "" {
		log.Println("No healthy origin server available for async revalidation.")
		return
	}

	myChan := make(chan struct{})
	_, loaded := p.cacheManager.leaderMap.LoadOrStore(key, myChan)

	if !loaded { // I am the leader.
		fmt.Println("Cache stale. I am the leader, revalidating in the background...")
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()

			time.Sleep(10 * time.Second)

			dataCopy := *currentData
			if err := revalidateAndUpdateCache(bgCtx, p.cacheManager.redisClient, key, &dataCopy, p.origin, portToUse, r); err != nil {
				log.Printf("Error during async cache revalidation: %v", err)
			}
			p.cacheManager.leaderMap.Delete(key)
			fmt.Println("Async leader finished revalidation and cleaned up.")
		}()
	} else {
		fmt.Println("I am a follower and I am waiting")
	}
}

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
	return "cache:" + r.Method + ":" + origin + path + "?" + r.URL.RawQuery
}

func (cache *CacheControl) getCacheControlDataFromHeader(headerValue string) {
	parts := strings.Split(headerValue, ",")
	for _, part := range parts {
		trimmedPart := strings.TrimSpace(part)
		isStaleWhileRevalidateDir := strings.HasPrefix(trimmedPart, "stale-while-revalidate=")
		staleIfErrorDir := strings.HasPrefix(trimmedPart, "stale-if-error=")
		if strings.Contains(trimmedPart, constant.MUST_REVALIDATE) {
			// Save Must Revalidate
			cache.cacheType = constant.MUST_REVALIDATE
		} else if strings.HasPrefix(trimmedPart, "max-age=") {
			// Save Max Age
			value, err := strconv.Atoi(strings.Split(trimmedPart, "=")[1])
			if err == nil {
				cache.maxAge = value
			}
		} else if staleIfErrorDir || isStaleWhileRevalidateDir {
			value, err := strconv.Atoi(strings.Split(trimmedPart, "=")[1])
			if err == nil {
				if isStaleWhileRevalidateDir {
					cache.cacheType = constant.STALE_WHILE_REVALIDATE
				} else {
					cache.cacheType = constant.STALE_IF_ERROR

				}
				cache.serveStaleUpto = value
			}
		}
	}
}

func fetchFromOrigin(ctx context.Context, origin string, etag string, r *http.Request) (string, int, map[string]string, error) {
	url := origin + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

func revalidateAndUpdateCache(ctx context.Context, redisClient *redis.Client, key string, cachedData *Cache, origin, portToUse string, r *http.Request) error {
	fullOrigin := origin + portToUse
	etag := cachedData.Headers["Etag"]
	newBody, statusCode, newHeaders, err := fetchFromOrigin(ctx, fullOrigin, etag, r)
	if err != nil {
		return fmt.Errorf("failed to revalidate from origin: %w", err)
	}

	fmt.Println("Revalidation response status code:", statusCode)

	switch statusCode {
	case http.StatusNotModified:
		fmt.Println("Origin returned 304 Not Modified. Renewing cache.")
		cachedData.StoredAt = time.Now().Unix()
	case http.StatusOK:
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

func CheckHealth(ports []string, origin string, healthCheckMap map[string]bool) {
	for _, port := range ports {
		fullOrigin := origin + port
		// Use a timeout for health checks
		client := http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(fullOrigin + "/health-check")
		if err != nil || resp.StatusCode != http.StatusOK {
			healthCheckMap[port] = false
		} else {
			healthCheckMap[port] = true
			resp.Body.Close()
		}
	}
}

func main() {
	originPorts := flag.String("ports", "8000", "Comma-separated list of origin server ports")
	origin := flag.String("origin", "http://localhost:", "Default Origin server URL")
	flag.Parse()

	if *origin == "" {
		log.Fatal("origin required")
	}

	ports := strings.Split(*originPorts, ",")
	healthCheckMap := make(map[string]bool)

	CheckHealth(ports, *origin, healthCheckMap)
	fmt.Printf("Initial health check results: %v\n", healthCheckMap)

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

	cacheManager := NewCacheManager(redisClient)
	proxyHandler := NewProxy(*origin, healthCheckMap, cacheManager)

	http.Handle("/", proxyHandler)

	fmt.Println("Proxy server started successfully at :3000")
	if err := http.ListenAndServe(":3000", nil); err != nil {
		log.Fatalf("Error starting the proxy server: %v", err)
	}
}