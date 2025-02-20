package cloudfrontgate

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Test ipstore.Contains
func Test_ipstore_Contains(t *testing.T) {
	testCases := []struct {
		name       string
		trustedIPs []string
		ip         net.IP
		want       bool
	}{
		{
			name:       "Test 1",
			trustedIPs: []string{"1.1.1.1/32"},
			ip:         net.ParseIP("1.1.1.1"),
			want:       true,
		},
		{
			name:       "Test 2",
			trustedIPs: []string{"1.1.1.2/32"},
			ip:         net.ParseIP("1.1.1.1"),
			want:       false,
		},
		{
			name:       "Test 3",
			trustedIPs: []string{"1.1.1.0/24"},
			ip:         net.ParseIP("1.1.1.11"),
			want:       true,
		},
		{
			name:       "Test 4",
			trustedIPs: []string{"1.1.1.0/24"},
			ip:         net.ParseIP("1.1.2.1"),
			want:       false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ips := newIPStore(CFAPI)
			ipnets, err := parseCIDRs(tc.trustedIPs)
			if err != nil {
				t.Errorf("parseCIDRs() = %v", err)
			}
			ips.Store(ipnets)

			if got := ips.Contains(tc.ip); got != tc.want {
				t.Errorf("ipstore.Contains() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIPStoreUpdate(t *testing.T) {
	tests := []struct {
		name          string
		mockResponse  string
		expectedCIDRs []string
		expectedError bool
	}{
		{
			name: "Valid response",
			mockResponse: `{
                "CLOUDFRONT_GLOBAL_IP_LIST": ["120.52.22.96/27", "205.251.249.0/24", "180.163.57.128/26"],
				"CLOUDFRONT_REGIONAL_EDGE_IP_LIST": ["13.113.196.64/26", "13.113.203.0/24", "52.199.127.192/26"]
            }`,
			expectedCIDRs: []string{"120.52.22.96/27", "205.251.249.0/24", "180.163.57.128/26", "13.113.196.64/26", "13.113.203.0/24", "52.199.127.192/26"},
			expectedError: false,
		},
		{
			name: "Invalid JSON response",
			mockResponse: `{
                "CLOUDFRONT_GLOBAL_IP_LIST": ["120.52.22.96/27", "205.251.249.0/24", "180.163.57.128/26"]
				"CLOUDFRONT_REGIONAL_EDGE_IP_LIST": ["13.113.196.64/26", "13.113.203.0/24", "52.199.127.192/26"
            }`,
			expectedCIDRs: nil,
			expectedError: true,
		},
		{
			name: "Empty CIDRs",
			mockResponse: `{
                "CLOUDFRONT_GLOBAL_IP_LIST": [],
                "CLOUDFRONT_REGIONAL_EDGE_IP_LIST": []
            }`,
			expectedCIDRs: []string{},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte(tt.mockResponse))
				if err != nil {
					t.Fatalf("Write() = %v", err)
				}
			}))
			defer server.Close()

			ips := newIPStore(server.URL)

			ctx := createContext(context.Background(), 5, []net.IPNet{})
			err := ips.Update(ctx)
			if (err != nil) != tt.expectedError {
				t.Fatalf("Update() error = %v, expectedError %v", err, tt.expectedError)
			}

			if !tt.expectedError {
				cidrs, ok := ips.Load().([]net.IPNet)
				if !ok {
					t.Fatalf("Failed to load CIDRs")
				}
				if len(cidrs) != len(tt.expectedCIDRs) {
					t.Fatalf("Expected %d CIDRs, got %d", len(tt.expectedCIDRs), len(cidrs))
				}

				for i, cidr := range tt.expectedCIDRs {
					_, expectedIPNet, _ := net.ParseCIDR(cidr)
					if !cidrs[i].IP.Equal(expectedIPNet.IP) || cidrs[i].Mask.String() != expectedIPNet.Mask.String() {
						t.Errorf("Expected CIDR %s, got %s", expectedIPNet.String(), cidrs[i].String())
					}
				}
			}
		})
	}
}

func TestCloudFrontGate_ServeHTTP(t *testing.T) {
	tests := []struct {
		name           string
		remoteAddr     string
		cidrs          []string
		expectedStatus int
	}{
		{
			name:           "Valid IP",
			remoteAddr:     "173.245.48.1:12345",
			cidrs:          []string{"173.245.48.0/20"},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Invalid IP",
			remoteAddr:     "192.168.1.1:12345",
			cidrs:          []string{"173.245.48.0/20"},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "Invalid IP format",
			remoteAddr:     "invalid-ip",
			cidrs:          []string{"173.245.48.0/20"},
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock next handler
			nextHandler := http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				rw.WriteHeader(http.StatusOK)
			})

			// Create IPStore and add CIDRs
			ips := newIPStore("")
			ipNets, err := parseCIDRs(tt.cidrs)
			if err != nil {
				t.Fatalf("parseCIDRs() = %v", err)
			}
			ips.Store(ipNets)

			// Create CloudFrontGate instance
			cf := &CloudFrontGate{
				ips:  ips,
				next: nextHandler,
			}

			// Create request and response recorder
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			req.RemoteAddr = tt.remoteAddr
			rw := httptest.NewRecorder()

			// Call ServeHTTP
			cf.ServeHTTP(rw, req)

			// Check response status
			if rw.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rw.Code)
			}
		})
	}
}

func TestCloudFrontGate_refreshLoop(t *testing.T) {
	tests := []struct {
		name            string
		refreshInterval time.Duration
		trustedIPs      []string
		mockResponse    string
		expectedCIDRs   []string
		expectedError   bool
	}{
		{
			name:            "Valid update",
			refreshInterval: 1 * time.Second,
			trustedIPs:      []string{"192.168.1.0/24"},
			mockResponse: `{
                "CLOUDFRONT_GLOBAL_IP_LIST": ["120.52.22.96/27", "205.251.249.0/24", "180.163.57.128/26"],
				"CLOUDFRONT_REGIONAL_EDGE_IP_LIST": ["13.113.196.64/26", "13.113.203.0/24", "52.199.127.192/26"]
            }`,
			expectedCIDRs: []string{"192.168.1.0/24", "120.52.22.96/27", "205.251.249.0/24", "180.163.57.128/26", "13.113.196.64/26", "13.113.203.0/24", "52.199.127.192/26"},
			expectedError: false,
		},
		{
			name:            "Invalid JSON response",
			refreshInterval: 1 * time.Second,
			trustedIPs:      []string{"192.168.1.0/24"},
			mockResponse: `{
                "CLOUDFRONT_GLOBAL_IP_LIST": ["120.52.22.96/27", "205.251.249.0/24", "180.163.57.128/26"]
				"CLOUDFRONT_REGIONAL_EDGE_IP_LIST": ["13.113.196.64/26", "13.113.203.0/24", "52.199.127.192/26"
            }`,
			expectedCIDRs: nil,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock IPStore
			ips := newIPStore("")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte(tt.mockResponse))
				if err != nil {
					t.Fatalf("Write() = %v", err)
				}
			}))
			defer server.Close()

			ips.cfAPI = server.URL

			// Parse trusted IPs
			var trustedIPNets []net.IPNet
			for _, cidr := range tt.trustedIPs {
				_, ipNet, _ := net.ParseCIDR(cidr)
				trustedIPNets = append(trustedIPNets, *ipNet)
			}

			// Create CloudFrontGate instance
			cf := &CloudFrontGate{
				ips:             ips,
				refreshInterval: tt.refreshInterval,
				trustedIPs:      trustedIPNets,
			}

			// Create context with cancel
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Run refreshLoop in a separate goroutine
			go cf.refreshLoop(ctx)

			// Wait for a bit more than the refresh interval to ensure the loop runs
			time.Sleep(tt.refreshInterval + 500*time.Millisecond)

			// Cancel the context to stop the loop
			cancel()

			// Check the updated CIDRs
			cidrs, ok := ips.Load().([]net.IPNet)
			if !ok {
				t.Fatalf("Failed to load CIDRs")
			}
			if len(cidrs) != len(tt.expectedCIDRs) {
				t.Fatalf("Expected %d CIDRs, got %d", len(tt.expectedCIDRs), len(cidrs))
			}

			for i, cidr := range tt.expectedCIDRs {
				_, expectedIPNet, _ := net.ParseCIDR(cidr)
				if !cidrs[i].IP.Equal(expectedIPNet.IP) || cidrs[i].Mask.String() != expectedIPNet.Mask.String() {
					t.Errorf("Expected CIDR %s, got %s", expectedIPNet.String(), cidrs[i].String())
				}
			}
		})
	}
}

func TestNewCloudFrontGate(t *testing.T) {
	tests := []struct {
		name          string
		config        *Config
		expectedError bool

		expectedNext    http.Handler
		expectedRefresh time.Duration
	}{
		{
			name: "Valid config",
			config: &Config{
				RefreshInterval: "1m",
				AllowedIPs:      []string{"173.245.48.0/20", "192.168.1.1"},
			},
			expectedError: false,

			expectedNext:    http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}),
			expectedRefresh: 1 * time.Minute,
		},
		{
			name: "Invalid refresh interval",
			config: &Config{
				RefreshInterval: "invalid",
				AllowedIPs:      []string{"173.245.48.0/20"},
			},
			expectedError: true,
		},
		{
			name: "Invalid allowed IPs",
			config: &Config{
				RefreshInterval: "1m",
				AllowedIPs:      []string{"invalid-ip"},
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

			cfHandler, err := New(context.Background(), nextHandler, tt.config, "test")
			if tt.expectedError {
				if err == nil {
					t.Errorf("Expected error, got nil")
				}
				return
			}

			cf, ok := cfHandler.(*CloudFrontGate)
			if !ok {
				t.Fatalf("New() = %v", cfHandler)
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if cf.next == nil {
				t.Errorf("Expected next handler, got nil")
			}

			if cf.refreshInterval != tt.expectedRefresh {
				t.Errorf("Expected refresh interval %v, got %v", tt.expectedRefresh, cf.refreshInterval)
			}

			if len(cf.trustedIPs) != len(tt.config.AllowedIPs) {
				t.Errorf("Expected %d trusted IPs, got %d", len(tt.config.AllowedIPs), len(cf.trustedIPs))
			}
		})
	}
}
