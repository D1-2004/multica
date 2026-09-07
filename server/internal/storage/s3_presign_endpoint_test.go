package storage

import "testing"

// A presigned URL is always handed to something outside the server's network,
// so it has to be signed against a host that thing can reach. Pre-release's own
// endpoint is VPC-internal, and a URL signed against it resolves nowhere.
func TestPublicEndpointForStripsTheInternalSuffix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "aliyun oss internal endpoint",
			in:   "https://oss-cn-zhangjiakou-internal.aliyuncs.com",
			want: "https://oss-cn-zhangjiakou.aliyuncs.com",
		},
		{
			name: "trailing slash is ignored",
			in:   "https://oss-cn-hangzhou-internal.aliyuncs.com/",
			want: "https://oss-cn-hangzhou.aliyuncs.com",
		},
		{
			// Already public: no second client, no behaviour change.
			name: "public endpoint needs no rewrite",
			in:   "https://oss-cn-zhangjiakou.aliyuncs.com",
			want: "",
		},
		{
			name: "unset endpoint",
			in:   "",
			want: "",
		},
		{
			// A host that merely contains the word must not be rewritten; only
			// the region-suffix form is Aliyun's internal convention.
			name: "unrelated host is left alone",
			in:   "https://internal-minio.example.com",
			want: "",
		},
		{
			name: "http is handled too",
			in:   "http://oss-cn-beijing-internal.aliyuncs.com",
			want: "http://oss-cn-beijing.aliyuncs.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicEndpointFor(tc.in); got != tc.want {
				t.Errorf("publicEndpointFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
