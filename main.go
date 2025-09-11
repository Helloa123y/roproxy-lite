package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
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

	if err := fasthttp.ListenAndServe(":"+port, requestHandler); err != nil {
		panic(err)
	}
}

func loadProxiesFromGeoNode() {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	apiUrl := "https://proxylist.geonode.com/api/proxy-list?limit=50&sort_by=lastChecked&sort_type=desc"
	req.SetRequestURI(apiUrl)
	req.Header.SetMethod("GET")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	apiClient := &fasthttp.Client{
		ReadTimeout: 15 * time.Second,
	}

	err := apiClient.Do(req, resp)
	if err != nil {
		loadDefaultProxies()
		return
	}

	if resp.StatusCode() != 200 {
		loadDefaultProxies()
		return
	}

	var geoNodeResponse GeoNodeResponse
	if err := json.Unmarshal(resp.Body(), &geoNodeResponse); err != nil {
		loadDefaultProxies()
		return
	}

	// Nur funktionierende Proxies
	var goodProxies []Proxy
	for _, proxy := range geoNodeResponse.Data {
		if hasValidProtocol(proxy.Protocols) {
			goodProxies = append(goodProxies, proxy)
		}
	}

	proxies = goodProxies
}

func hasValidProtocol(protocols []string) bool {
	for _, protocol := range protocols {
		if protocol == "http" || protocol == "https" {
			return true
		}
	}
	return false
}

func loadDefaultProxies() {
	proxies = []Proxy{
		{IP: "104.16.202.9", Port: "80", Protocols: []string{"http"}, Country: "CA"},
		{IP: "104.21.237.193", Port: "80", Protocols: []string{"http"}, Country: "CA"},
	}
}

func getRandomProxy() *Proxy {
	if len(proxies) == 0 {
		return nil
	}
	rand.Seed(time.Now().UnixNano())
	return &proxies[rand.Intn(len(proxies))]
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	// Debug-Info in Response header
	ctx.Response.Header.Set("X-Proxy-Count", strconv.Itoa(len(proxies)))
	ctx.Response.Header.Set("X-Server-Time", time.Now().Format("15:04:05"))

	// URL Validation
	path := string(ctx.RequestURI())[1:]
	if path == "" {
		sendDebugResponse(ctx, 400, "Please provide a URL path. Usage: /subdomain/path", nil)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		sendDebugResponse(ctx, 400, "URL format invalid. Expected: /subdomain/path", nil)
		return
	}

	response, debugInfo := makeRequestWithDebug(ctx, 1)
	defer fasthttp.ReleaseResponse(response)

	// Debug-Info zu Response hinzufügen
	ctx.Response.Header.Set("X-Attempts", strconv.Itoa(debugInfo.Attempts))
	ctx.Response.Header.Set("X-Proxy-Used", strconv.FormatBool(debugInfo.UsedProxy))
	if debugInfo.Proxy != nil {
		ctx.Response.Header.Set("X-Proxy-IP", debugInfo.Proxy.IP)
		ctx.Response.Header.Set("X-Proxy-Port", debugInfo.Proxy.Port)
	}

	ctx.SetStatusCode(response.StatusCode())
	ctx.SetBody(response.Body())
	response.Header.VisitAll(func(key, value []byte) {
		ctx.Response.Header.Set(string(key), string(value))
	})
}

type DebugInfo struct {
	Attempts   int
	UsedProxy  bool
	Proxy      *Proxy
	Error      string
	Duration   time.Duration
	TargetURL  string
}

func makeRequestWithDebug(ctx *fasthttp.RequestCtx, attempt int) (*fasthttp.Response, *DebugInfo) {
	debugInfo := &DebugInfo{
		Attempts: attempt,
		TargetURL: "https://" + string(ctx.RequestURI())[1:],
	}

	if attempt > retries {
		debugInfo.Error = fmt.Sprintf("Max retries exceeded (%d)", retries)
		resp := fasthttp.AcquireResponse()
		resp.SetStatusCode(502)
		resp.SetBody([]byte(fmt.Sprintf("Proxy failed after %d attempts. Debug: %s", retries, debugInfo.Error)))
		return resp, debugInfo
	}

	// Immer erst direkten Versuch, dann Proxy
	useProxy := attempt > 1 && len(proxies) > 0
	debugInfo.UsedProxy = useProxy

	var proxy *Proxy
	if useProxy {
		proxy = getRandomProxy()
		debugInfo.Proxy = proxy
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	path := string(ctx.RequestURI())[1:]
	parts := strings.SplitN(path, "/", 2)
	targetURL := "https://" + parts[0] + ".roblox.com/" + parts[1]

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
		proxyURL := proxy.IP + ":" + proxy.Port
		dial := func(addr string) (net.Conn, error) {
			return net.DialTimeout("tcp", proxyURL, time.Duration(timeout)*time.Second)
		}

		proxyClient := &fasthttp.Client{
			ReadTimeout:  time.Duration(timeout) * time.Second,
			WriteTimeout: time.Duration(timeout) * time.Second,
			Dial:         dial,
		}

		err = proxyClient.Do(req, resp)
	} else {
		// Direkte Verbindung
		err = client.Do(req, resp)
	}

	debugInfo.Duration = time.Since(startTime)

	if err != nil {
		debugInfo.Error = err.Error()
		fasthttp.ReleaseResponse(resp)

		// Kurze Pause vor nächstem Versuch
		if attempt < retries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		return makeRequestWithDebug(ctx, attempt+1)
	}

	return resp, debugInfo
}

func sendDebugResponse(ctx *fasthttp.RequestCtx, statusCode int, message string, debugInfo *DebugInfo) {
	ctx.SetStatusCode(statusCode)
	
	response := map[string]interface{}{
		"error":   message,
		"status":  statusCode,
		"time":    time.Now().Format("2006-01-02 15:04:05"),
		"proxies": len(proxies),
	}

	if debugInfo != nil {
		response["debug"] = debugInfo
	}

	jsonResponse, _ := json.MarshalIndent(response, "", "  ")
	ctx.SetContentType("application/json")
	ctx.SetBody(jsonResponse)
}
