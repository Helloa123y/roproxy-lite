package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

var timeout, _ = strconv.Atoi(os.Getenv("TIMEOUT"))
var retries, _ = strconv.Atoi(os.Getenv("RETRIES"))
var port = os.Getenv("PORT")
var proxies []Proxy

type Proxy struct {
	ID       string   `json:"_id"`
	IP       string   `json:"ip"`
	Port     string   `json:"port"`
	Protocols []string `json:"protocols"`
	Latency  float64  `json:"latency"`
	UpTime   float64  `json:"upTime"`
	ASN      string   `json:"asn"`
	Country  string   `json:"country"`
	City     string   `json:"city"`
	ISP      string   `json:"isp"`
	Speed    int      `json:"speed"`
}

type GeoNodeResponse struct {
	Data []Proxy `json:"data"`
}

var client *fasthttp.Client

func main() {
	// Default Werte falls Umgebungsvariablen nicht gesetzt sind
	if timeout == 0 {
		timeout = 30
	}
	if retries == 0 {
		retries = 3
	}
	if port == "" {
		port = "8080"
	}

	// Proxies von GeoNode API laden
	loadProxiesFromGeoNode()

	// Proxies regelmäßig aktualisieren
	go refreshProxiesPeriodically()

	client = &fasthttp.Client{
		ReadTimeout:         time.Duration(timeout) * time.Second,
		MaxIdleConnDuration: 60 * time.Second,
	}

	log.Printf("🚀 Starting server on port %s with %d proxies loaded", port, len(proxies))
	
	if err := fasthttp.ListenAndServe(":"+port, requestHandler); err != nil {
		log.Fatalf("Error in ListenAndServe: %s", err)
	}
}

func loadProxiesFromGeoNode() {
	log.Printf("🌐 Loading proxies from GeoNode API...")
	
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// GeoNode API URL mit Parametern
	apiUrl := "https://proxylist.geonode.com/api/proxy-list?limit=500&page=1&sort_by=lastChecked&sort_type=desc"
	req.SetRequestURI(apiUrl)
	req.Header.SetMethod("GET")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "RoProxy/1.0")

	apiClient := &fasthttp.Client{
		ReadTimeout: 15 * time.Second,
	}

	err := apiClient.Do(req, resp)
	if err != nil {
		log.Printf("❌ Failed to load proxies from GeoNode API: %v", err)
		loadDefaultProxies()
		return
	}

	if resp.StatusCode() != 200 {
		log.Printf("❌ GeoNode API returned status: %d", resp.StatusCode())
		loadDefaultProxies()
		return
	}

	var geoNodeResponse GeoNodeResponse
	if err := json.Unmarshal(resp.Body(), &geoNodeResponse); err != nil {
		log.Printf("❌ Failed to parse GeoNode API response: %v", err)
		log.Printf("Response body: %s", string(resp.Body()))
		loadDefaultProxies()
		return
	}

	// Filtere nur HTTP/SOCKS Proxies
	var validProxies []Proxy
	for _, proxy := range geoNodeResponse.Data {
		if hasValidProtocol(proxy.Protocols) {
			validProxies = append(validProxies, proxy)
		}
	}

	proxies = validProxies
	log.Printf("✅ Successfully loaded %d proxies from GeoNode API (%d total, %d valid)", 
		len(proxies), len(geoNodeResponse.Data), len(validProxies))
	
	if len(proxies) > 0 {
		log.Printf("📊 Sample proxy: %s:%s (%s) - %s", 
			proxies[0].IP, proxies[0].Port, strings.Join(proxies[0].Protocols, ","), proxies[0].Country)
	}
}

func hasValidProtocol(protocols []string) bool {
	for _, protocol := range protocols {
		if protocol == "http" || protocol == "socks4" || protocol == "socks5" {
			return true
		}
	}
	return false
}

func loadDefaultProxies() {
	log.Printf("⚠️  Using default fallback proxies")
	proxies = []Proxy{
		{IP: "104.16.202.9", Port: "80", Protocols: []string{"http"}, Country: "CA"},
		{IP: "62.210.201.140", Port: "17937", Protocols: []string{"socks4"}, Country: "FR"},
		{IP: "5.78.46.108", Port: "8080", Protocols: []string{"socks4"}, Country: "US"},
		{IP: "104.21.237.193", Port: "80", Protocols: []string{"http"}, Country: "CA"},
		{IP: "201.48.235.33", Port: "4153", Protocols: []string{"socks4"}, Country: "BR"},
	}
}

func refreshProxiesPeriodically() {
	for {
		time.Sleep(30 * time.Minute) // Alle 30 Minuten aktualisieren
		loadProxiesFromGeoNode()
	}
}

func getRandomProxy() *Proxy {
	if len(proxies) == 0 {
		log.Printf("⚠️  No proxies available, using direct connection")
		return nil
	}
	rand.Seed(time.Now().UnixNano())
	proxy := proxies[rand.Intn(len(proxies))]
	
	// Protokoll-Präferenz: HTTP > SOCKS5 > SOCKS4
	protocol := "http"
	for _, p := range proxy.Protocols {
		if p == "http" {
			protocol = "http"
			break
		} else if p == "socks5" {
			protocol = "socks5"
		} else if p == "socks4" {
			protocol = "socks4"
		}
	}
	
	// Für Debugging
	log.Printf("🎲 Selected proxy: %s:%s (%s) - %s", proxy.IP, proxy.Port, protocol, proxy.Country)
	
	return &proxy
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	val, ok := os.LookupEnv("KEY")

	if ok && string(ctx.Request.Header.Peek("PROXYKEY")) != val {
		ctx.SetStatusCode(407)
		ctx.SetBody([]byte("Missing or invalid PROXYKEY header."))
		return
	}

	if len(strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)) < 2 {
		ctx.SetStatusCode(400)
		ctx.SetBody([]byte("URL format invalid."))
		return
	}

	response := makeRequest(ctx, 1)
	defer fasthttp.ReleaseResponse(response)

	ctx.SetStatusCode(response.StatusCode())
	ctx.SetBody(response.Body())
	response.Header.VisitAll(func(key, value []byte) {
		ctx.Response.Header.Set(string(key), string(value))
	})
}

func makeRequest(ctx *fasthttp.RequestCtx, attempt int) *fasthttp.Response {
	if attempt > retries {
		resp := fasthttp.AcquireResponse()
		resp.SetBody([]byte("Proxy failed to connect after " + strconv.Itoa(retries) + " attempts. Please try again."))
		resp.SetStatusCode(500)
		return resp
	}

	proxy := getRandomProxy()
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	
	urlParts := strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)
	targetURL := "https://" + urlParts[0] + ".roblox.com/" + urlParts[1]
	
	req.SetRequestURI(targetURL)
	req.Header.SetMethod(string(ctx.Method()))
	req.SetBody(ctx.Request.Body())
	
	// Headers kopieren
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		if string(key) != "Host" && string(key) != "Connection" {
			req.Header.Set(string(key), string(value))
		}
	})
	
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Del("Roblox-Id")
	req.Header.Del("Host")

	resp := fasthttp.AcquireResponse()
	var err error

	if proxy != nil {
		// Proxy URL erstellen (nur für HTTP Proxies)
		proxyURL := "http://" + proxy.IP + ":" + proxy.Port
		log.Printf("🔗 Using proxy: %s for %s", proxyURL, targetURL)
		
		// Custom Dialer für Proxy
		dial := func(addr string) (net.Conn, error) {
			return fasthttp.DialTimeout(proxyURL, time.Duration(timeout)*time.Second)
		}
		
		proxyClient := &fasthttp.Client{
			ReadTimeout: time.Duration(timeout) * time.Second,
			Dial:        dial,
		}
		
		err = proxyClient.Do(req, resp)
	} else {
		// Direkte Verbindung
		err = client.Do(req, resp)
	}

	if err != nil {
		log.Printf("❌ Attempt %d/%d failed: %v", attempt, retries, err)
		fasthttp.ReleaseResponse(resp)
		time.Sleep(time.Duration(attempt) * time.Second)
		return makeRequest(ctx, attempt+1)
	} else {
		log.Printf("✅ Request successful via proxy: %t, Status: %d", proxy != nil, resp.StatusCode())
		return resp
	}
}
