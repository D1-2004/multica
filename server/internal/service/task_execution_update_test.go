package service

import "testing"

func TestTaskExecutionUpdateResultMessageUsesPersistedProviderOutput(t *testing.T) {
	tests := []struct {
		name   string
		result []byte
		want   string
	}{
		{
			name:   "ordinary final output without DWS reply",
			result: []byte(`{"output":"任务已转入后台\\n第二行"}`),
			want:   "任务已转入后台\n第二行",
		},
		{
			name:   "legacy DWS receipt does not override provider output",
			result: []byte(`{"output":"provider final output","result_message":"旧 DWS 工具回执"}`),
			want:   "provider final output",
		},
		{
			name:   "legacy DWS receipt without provider output",
			result: []byte(`{"result_message":"旧 DWS 工具回执"}`),
		},
		{
			name:   "invalid result",
			result: []byte(`not-json`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskExecutionUpdateResultMessage(tt.result); got != tt.want {
				t.Fatalf("taskExecutionUpdateResultMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}
