package service

import "testing"

func TestTaskExecutionUpdateResultMessageUsesPersistedDaemonReceipt(t *testing.T) {
	tests := []struct {
		name   string
		result []byte
		want   string
	}{
		{
			name:   "successful DWS reply text",
			result: []byte(`{"output":"provider final output","result_message":"任务已转入后台\\n第二行"}`),
			want:   "任务已转入后台\n第二行",
		},
		{
			name:   "provider output is not a DWS receipt",
			result: []byte(`{"output":"provider final output"}`),
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
