package server

/*
func Test_parseTime(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name      string
		args      args
		want      time.Time
		wantError bool
	}{
		{
			name: "parse time ``",
			args: args{"0"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC),
		},
		{
			name: "parse time missing timezone",
			args: args{"8p"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC),
		},
		{
			name: "parse time 0",
			args: args{"0"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC),
		},
		{
			name: "parse minutes from now",
			args: args{"+5m"},
			want: time.Now().Add(5 * time.Minute),
		},
		{
			name: "parse time 8p est",
			args: args{"8p est"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 20, 0, 0, 0, time.UTC),
		},
		{
			name: "parse time 8p CST",
			args: args{"8p CST"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 20, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.args.s)
			if err != nil {
				if !tt.wantError {
					t.Errorf("parseTime() error = %v, wantErr %v", err, tt.wantError)
				} else {
					if !reflect.DeepEqual(got, tt.want) {
						t.Errorf("parseTime() = %v, want %v", got, tt.want)
					}
				}
			}
		})

	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.args.s)

			if err != nil {
				if !tt.wantError {
					t.Errorf("parseTime() error = %v, wantErr %v", err, tt.wantError)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseTime() = %v, want %v", got, tt.want)
			}
		})
	}
}
*/
