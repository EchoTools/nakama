package server

import "testing"

// TestIPInfoIsDataCenter covers the fact that both providers already fetched
// and both then dropped: a datacenter address is a DIFFERENT fact from a proxy
// address, and IsVPN() does not see it.
//
// The cases that matter are the ones where the two disagree. A rented VPS used
// as a game proxy is commonly hosting-but-not-proxy, and that combination read
// as entirely clean before this existed.
func TestIPInfoIsDataCenter(t *testing.T) {
	t.Run("IPAPI", func(t *testing.T) {
		tests := []struct {
			name           string
			resp           IPAPIResponse
			wantDataCenter bool
			wantVPN        bool
		}{
			{
				// The gap, stated as a test.
				name:           "HostingButNotProxy",
				resp:           IPAPIResponse{Hosting: true, Proxy: false},
				wantDataCenter: true,
				wantVPN:        false,
			},
			{
				name:           "ProxyButNotHosting",
				resp:           IPAPIResponse{Hosting: false, Proxy: true},
				wantDataCenter: false,
				wantVPN:        true,
			},
			{
				name:           "Both",
				resp:           IPAPIResponse{Hosting: true, Proxy: true},
				wantDataCenter: true,
				wantVPN:        true,
			},
			{
				name:           "Residential",
				resp:           IPAPIResponse{},
				wantDataCenter: false,
				wantVPN:        false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				d := &ipapiData{Response: tt.resp}
				if got := d.IsDataCenter(); got != tt.wantDataCenter {
					t.Errorf("IsDataCenter() = %v, want %v", got, tt.wantDataCenter)
				}
				// Pinned alongside, so that widening IsVPN to include hosting
				// is a deliberate edit to this test rather than a silent
				// enforcement change. That decision is open on #516.
				if got := d.IsVPN(); got != tt.wantVPN {
					t.Errorf("IsVPN() = %v, want %v", got, tt.wantVPN)
				}
			})
		}
	})

	t.Run("IPQS", func(t *testing.T) {
		tests := []struct {
			name           string
			connectionType string
			want           bool
		}{
			{name: "DocumentedSpelling", connectionType: "Data Center", want: true},
			{name: "NoSpace", connectionType: "DataCenter", want: true},
			{name: "Lowercase", connectionType: "data center", want: true},
			{name: "Residential", connectionType: "Residential", want: false},
			{name: "Corporate", connectionType: "Corporate", want: false},
			{name: "Mobile", connectionType: "Mobile", want: false},
			// An absent value must not read as datacenter. IPQS omits the
			// field on some plans, and "unknown" is not "yes".
			{name: "Absent", connectionType: "", want: false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				d := &IPQSData{Response: IPQSResponse{ConnectionType: tt.connectionType}}
				if got := d.IsDataCenter(); got != tt.want {
					t.Errorf("IsDataCenter(%q) = %v, want %v", tt.connectionType, got, tt.want)
				}
			})
		}
	})

	// A known shared provider is a false positive for both questions, which is
	// what the shared-provider list exists to prevent. Whatever the flags say,
	// a shared provider must not be reported as a datacenter.
	t.Run("SharedProviderShortCircuits", func(t *testing.T) {
		// Taken from the real list rather than invented, so this fails if the
		// list is emptied rather than passing vacuously against a string
		// nothing recognises.
		if len(knownSharedIPProviders) == 0 {
			t.Fatal("knownSharedIPProviders is empty; this test would prove nothing")
		}
		shared := knownSharedIPProviders[0]
		if !isKnownSharedIPProvider(shared, "") {
			t.Fatalf("precondition failed: %q is not recognised as a shared provider", shared)
		}

		ipapi := &ipapiData{Response: IPAPIResponse{Hosting: true, ISP: shared, Organization: shared}}
		if ipapi.IsDataCenter() {
			t.Errorf("ipapi IsDataCenter() = true for known shared provider %q", shared)
		}

		ipqs := &IPQSData{Response: IPQSResponse{ConnectionType: "Data Center", ISP: shared, Organization: shared}}
		if ipqs.IsDataCenter() {
			t.Errorf("ipqs IsDataCenter() = true for known shared provider %q", shared)
		}
	})

	t.Run("StubIsNeverDataCenter", func(t *testing.T) {
		if (StubIPInfo{}).IsDataCenter() {
			t.Error("StubIPInfo.IsDataCenter() = true; the stub must claim nothing")
		}
	})
}
