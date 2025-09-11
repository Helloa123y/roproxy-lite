package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	"golang.org/x/net/proxy"
)

var timeout = 30
var retries = 3
var port = "8080"
var proxies []Proxy

type Proxy struct {
	ID        string   `json:"_id"`
	IP        string   `json:"ip"`
	Port      string   `json:"port"`
	Protocols []string `json:"protocols"`
	Latency   float64  `json:"latency"`
	UpTime    float64  `json:"upTime"`
	ASN       string   `json:"asn"`
	Country   string   `json:"country"`
	City      string   `json:"city"`
	ISP       string   `json:"isp"`
	Speed     int      `json:"speed"`
}

type GeoNodeResponse struct {
	Data []Proxy `json:"data"`
}

var client *fasthttp.Client

func main() {
	// Umgebungsvariablen lesen
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}
	if envTimeout := os.Getenv("TIMEOUT"); envTimeout != "" {
		if t, err := strconv.Atoi(envTimeout); err == nil {
			timeout = t
		}
	}
	if envRetries := os.Getenv("RETRIES"); envRetries != "" {
		if r, err := strconv.Atoi(envRetries); err == nil {
			retries = r
		}
	}

	// Proxies laden
	loadProxiesFromGeoNode()

	// Haupt-Client für direkte Verbindungen
	client = &fasthttp.Client{
		ReadTimeout:         time.Duration(timeout) * time.Second,
		WriteTimeout:        time.Duration(timeout) * time.Second,
		MaxIdleConnDuration: 60 * time.Second,
		Dial: (&fasthttp.TCPDialer{
			Concurrency: 1000,
		}).Dial,
	}

	log.Printf("🚀 Starting server on port %s", port)
	log.Printf("⚙️  Configuration: Timeout=%ds, Retries=%d, Proxies=%d", timeout, retries, len(proxies))

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

	apiUrl := "https://proxylist.geonode.com/api/proxy-list?limit=100&sort_by=lastChecked&sort_type=desc&protocols=socks4,socks5,https"
	req.SetRequestURI(apiUrl)
	req.Header.SetMethod("GET")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	apiClient := &fasthttp.Client{
		ReadTimeout: 15 * time.Second,
	}

	err := apiClient.Do(req, resp)
	if err != nil {
		log.Printf("❌ Failed to connect to GeoNode API: %v", err)
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
		log.Printf("❌ Failed to parse JSON response: %v", err)
		loadDefaultProxies()
		return
	}

	// Nur funktionierende Proxies mit guter UpTime
	var goodProxies []Proxy
	for _, proxy := range geoNodeResponse.Data {
		if proxy.UpTime > 90 && hasValidProtocol(proxy.Protocols) && proxy.Latency < 1000 {
			goodProxies = append(goodProxies, proxy)
		}
	}

	// Nach Priorität sortieren
	goodProxies = sortProxiesByPriority(goodProxies)
	proxies = goodProxies
	
	log.Printf("✅ Loaded %d proxies (filtered from %d)", len(proxies), len(geoNodeResponse.Data))
	for i, p := range proxies {
		if i < 5 { // Zeige nur die ersten 5 an
			log.Printf("   %d. %s:%s (%s) - %v", i+1, p.IP, p.Port, p.Country, p.Protocols)
		}
	}

	if len(proxies) == 0 {
		log.Printf("⚠️  No good proxies found, using direct connections only")
	}
}

func hasValidProtocol(protocols []string) bool {
	for _, protocol := range protocols {
		if protocol == "socks4" || protocol == "socks5" || protocol == "https" || protocol == "http" {
			return true
		}
	}
	return false
}

func getProxyPriority(proxy *Proxy) int {
	// Priorität: SOCKS5 > SOCKS4 > HTTPS > HTTP
	for _, protocol := range proxy.Protocols {
		switch protocol {
		case "socks5":
			return 4
		case "socks4":
			return 3
		case "https":
			return 2
		case "http":
			return 1
		}
	}
	return 0
}

func sortProxiesByPriority(proxies []Proxy) []Proxy {
	sorted := make([]Proxy, len(proxies))
	copy(sorted, proxies)
	
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if getProxyPriority(&sorted[j]) > getProxyPriority(&sorted[i]) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted
}

func loadDefaultProxies() {
	log.Printf("⚠️  Using default fallback proxies")
	proxies = []Proxy{
		{IP: "104.16.202.9", Port: "80", Protocols: []string{"http"}, Country: "CA", UpTime: 100},
		{IP: "104.21.237.193", Port: "80", Protocols: []string{"http"}, Country: "CA", UpTime: 100},
	}
}

func getBestProxy() *Proxy {
	if len(proxies) == 0 {
		return nil
	}
	
	// Versuche die besten Proxies zuerst (sind schon sortiert)
	rand.Seed(time.Now().UnixNano())
	if len(proxies) > 3 {
		// Wähle zufällig aus den besten 25%
		topCount := len(proxies) / 4
		if topCount < 1 {
			topCount = 1
		}
		return &proxies[rand.Intn(topCount)]
	}
	
	return &proxies[rand.Intn(len(proxies))]
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	log.Printf("📨 Received request: %s %s", ctx.Method(), ctx.RequestURI())

	// URL Validation
	path := string(ctx.RequestURI())[1:]
	if path == "" {
		ctx.Error("Please provide a URL path", 400)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		ctx.Error("URL format invalid. Expected: /subdomain/path", 400)
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
		log.Printf("❌ MAX RETRIES EXCEEDED after %d attempts", retries)
		resp := fasthttp.AcquireResponse()
		resp.SetStatusCode(502)
		resp.SetBody([]byte("Proxy failed to connect. Please try again later."))
		return resp
	}

	// Immer erst direkten Versuch, dann Proxy
	useProxy := attempt > 1 && len(proxies) > 0
	var proxy *Proxy
	if useProxy {
		proxy = getBestProxy()
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	path := string(ctx.RequestURI())[1:]
	parts := strings.SplitN(path, "/", 2)
	targetURL := "https://" + parts[0] + ".roblox.com/" + parts[1]

	log.Printf("🔗 Attempt %d/%d: %s -> %s (Proxy: %t)", attempt, retries, ctx.RequestURI(), targetURL, useProxy)

	req.SetRequestURI(targetURL)
	req.Header.SetMethod(string(ctx.Method()))
	req.SetBody(ctx.Request.Body())

	// Headers setzen
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json, text/html, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Del("Host")
	req.Header.Del("Roblox-Id")

	resp := fasthttp.AcquireResponse()
	startTime := time.Now()

	var err error

	if useProxy && proxy != nil {
		log.Printf("🌐 Using proxy: %s:%s (%s) - Protocols: %v", proxy.IP, proxy.Port, proxy.Country, proxy.Protocols)
		
		// Proxy-Dialer basierend auf Protokoll
		proxyDialer, err := getProxyDialer(proxy)
		if err != nil {
			log.Printf("❌ Proxy dialer creation failed: %v", err)
			fasthttp.ReleaseResponse(resp)
			return makeRequest(ctx, attempt+1)
		}
		
		proxyClient := &fasthttp.Client{
			ReadTimeout:  time.Duration(timeout) * time.Second,
			WriteTimeout: time.Duration(timeout) * time.Second,
			Dial:         proxyDialer.Dial,
		}
		
		err = proxyClient.Do(req, resp)
	} else {
		// Direkte Verbindung
		log.Printf("🔗 Direct connection attempt")
		err = client.Do(req, resp)
	}

	duration := time.Since(startTime)

	if err != nil {
		log.Printf("❌ Attempt %d failed after %v: %v", attempt, duration, err)
		fasthttp.ReleaseResponse(resp)
		
		// Kurze Pause vor nächstem Versuch
		if attempt < retries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		
		return makeRequest(ctx, attempt+1)
	}

	log.Printf("✅ Success! Status: %d, Time: %v, Size: %d bytes", resp.StatusCode(), duration, len(resp.Body()))
	return resp
}

func getProxyDialer(p *Proxy) (proxy.Dialer, error) {
	proxyAddr := net.JoinHostPort(p.IP, p.Port)
	
	// Check for SOCKS proxies first
	for _, protocol := range p.Protocols {
		if protocol == "socks5" {
			log.Printf("   Using SOCKS5 proxy")
			return proxy.SOCKS5("tcp", proxyAddr, nil, &net.Dialer{
				Timeout: time.Duration(timeout) * time.Second,
			})
		}
		if protocol == "socks4" {
			log.Printf("   Using SOCKS4 proxy")
			return proxy.SOCKS4("tcp", proxyAddr, nil, &net.Dialer{
				Timeout: time.Duration(timeout) * time.Second,
			})
		}
	}
	
	// Fallback to HTTP proxy
	log.Printf("   Using HTTP proxy (with CONNECT)")
	return &httpConnectDialer{
		proxyAddr: proxyAddr,
		timeout:   time.Duration(timeout) * time.Second,
	}, nil
}

// HTTP CONNECT dialer implementation
type httpConnectDialer struct {
	proxyAddr string
	timeout   time.Duration
}

func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", d.proxyAddr, d.timeout)
	if err != nil {
		return nil, err
	}
	
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr)
	if _, err := conn.Write([]byte(connectReq)); err != nil {
		conn.Close()
		return nil, err
	}
	
	// Read response
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	
	if !strings.Contains(response, "200") {
		conn.Close()
		return nil, fmt.Errorf("CONNECT failed: %s", response)
	}
	
	// Read remaining headers
	for {
		line, err := reader.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	
	return conn, nil
}
