package server

import (
	"net"
	"reflect"
	"testing"
)

func Test_ipToKey(t *testing.T) {
	type args struct {
		ip net.IP
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "IPv4",
			args: args{
				ip: net.ParseIP("192.168.1.1"),
			},
			want: "rttc0a80101",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ipToKey(tt.args.ip); got != tt.want {
				t.Errorf("ipToKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_distributeParties(t *testing.T) {
	partyA1 := &MatchmakerEntry{}
	partyA2 := &MatchmakerEntry{}
	partyA3 := &MatchmakerEntry{}
	partyB1 := &MatchmakerEntry{}
	partyB2 := &MatchmakerEntry{}
	partyB3 := &MatchmakerEntry{}
	partyC1 := &MatchmakerEntry{}
	partyD1 := &MatchmakerEntry{}
	type args struct {
		parties [][]*MatchmakerEntry
	}
	tests := []struct {
		name string
		args args
		want [][]*MatchmakerEntry
	}{
		{
			name: "Test Case 1",
			args: args{
				parties: [][]*MatchmakerEntry{
					{
						partyA1,
						partyA2,
						partyA3,
					},
					{
						partyB1,
						partyB2,
						partyB3,
					},
					{
						partyC1,
					},
					{
						partyD1,
					},
				},
			},
			want: [][]*MatchmakerEntry{
				{
					partyA1,
					partyA2,
					partyA3,
					partyC1,
				},
				{
					partyB1,
					partyB2,
					partyB3,
					partyD1,
				},
			},
		},
		// Add more test cases here
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := distributeParties(tt.args.parties); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("distributeParties() = %v, want %v", got, tt.want)
			}
		})
	}
}

/*
func TestMatchmakingRegistry_buildMatch(t *testing.T) {

	consoleLogger := loggerForTest(t)
	matchMaker, cleanup, err := createTestMatchmaker(t, consoleLogger, true, nil)
	if err != nil {
		t.Fatalf("error creating test matchmaker: %v", err)
	}
	defer cleanup()

	sessionID, _ := uuid.NewV4()
	ticket, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
		{
			UserId:    "a",
			SessionId: "a",
			Username:  "a",
			Node:      "a",
			SessionID: sessionID,
		},
	}, sessionID.String(), "", "properties.a1:foo", 2, 2, 1, map[string]string{
		"a1": "bar",
	}, map[string]float64{})
	if err != nil {
		t.Fatalf("error matchmaker add: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected valid ticket")
	}

	matchRegistry, createFn, err := createTestMatchRegistry(t, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer matchRegistry.Stop(0)

	evrPipeline := NewEvrPipeline(logger, matchRegistry, metrics)

	type fields struct {
		RWMutex           sync.RWMutex
		ctx               context.Context
		ctxCancelFn       context.CancelFunc
		nk                runtime.NakamaModule
		db                *sql.DB
		logger            *zap.Logger
		matchRegistry     MatchRegistry
		metrics           Metrics
		config            Config
		evrPipeline       *EvrPipeline
		matchingBySession *MapOf[uuid.UUID, *MatchmakingSession]
		cacheByUserId     *MapOf[uuid.UUID, *LatencyCache]
	}
	type args struct {
		entrants []*MatchmakerEntry
		config   MatchmakingSettings
	}

	entrants := make([]*MatchmakerEntry, 0)
	for i := 0; i < 10; i++ {
		entrants = append(entrants, &MatchmakerEntry{
			Ticket: uuid.Must(uuid.NewV4()).String(),
			Presence: &MatchmakerPresence{
				UserId:    uuid.Must(uuid.NewV4()).String(),
				SessionId: uuid.Must(uuid.NewV4()).String(),
				Username:  fmt.Sprintf("user%d", i),
				Node:      "node1",
			},

			Properties: map[string]interface{}{},
			PartyId:    uuid.Must(uuid.NewV4()).String(),
			StringProperties: map[string]string{
				"147afc9d28194197926d5b3f92790edc": "T",
				"channel":                          "147afc9d28194197926d5b3f92790edc",
				"mode":                             "echo_arena",
				"userid":                           "5e239960dfb841aba8bd68804615cbd2",
			},
			NumericProperties: map[string]float64{
				"rtt039359e0": 41,
				"rtt3e44a77b": 137,
				"rtt44b9ef44": 89,
				"rtt48236ebf": 120,
				"rtt63fb6418": 74,
				"rttc6a68f36": 73,
			},
		})
	}

	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name:   "Test Case 1",
			fields: fields{},
			args: args{
				entrants: entrants,
				config:   MatchmakingSettings{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			matchMaker, cleanup, err := createTestMatchmaker(t, consoleLogger, true, nil)
			if err != nil {
				t.Fatalf("error creating test matchmaker: %v", err)
			}
			defer cleanup()
			matchmaker := NewLocalMatchmaker(logger, logger, config, router, metrics, nk)
			mr := NewMatchmakingRegistry(logger, matchRegistry, matchmaker, metrics, db, nk, config, evrPipeline)
			mr := &MatchmakingRegistry{
				ctx:               tt.fields.ctx,
				nk:                tt.fields.nk,
				db:                tt.fields.db,
				logger:            tt.fields.logger,
				matchRegistry:     tt.fields.matchRegistry,
				metrics:           tt.fields.metrics,
				config:            tt.fields.config,
				evrPipeline:       tt.fields.evrPipeline,
				matchingBySession: tt.fields.matchingBySession,
				cacheByUserId:     tt.fields.cacheByUserId,
			}
			mr.buildMatch(tt.args.entrants, tt.args.config)
		})
	}
}
*/

func TestSortByFrequency(t *testing.T) {
	type args struct {
		items []int
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		{
			name: "Test Case 1",
			args: args{
				items: []int{
					1,
					2,
					3,
					2,
					1,
					1,
					2,
					2,
				},
			},
			want: []int{
				2,
				1,
				3,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompactedFrequencySort(tt.args.items, true)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompactedFrequencySort() = %v, want %v", tt.args.items, tt.want)
			}
		})
	}
}
