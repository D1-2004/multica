package metrics

import "github.com/prometheus/client_golang/prometheus"

type dingTalkIntegrationMetrics struct {
	accountBegin              *prometheus.CounterVec
	accountCallback           *prometheus.CounterVec
	accountSubscriptionVerify *prometheus.CounterVec
	accountUnbind             *prometheus.CounterVec
	dispatchCredentialDerive  *prometheus.CounterVec
	dispatchAuth              *prometheus.CounterVec
}

func newDingTalkIntegrationMetrics() *dingTalkIntegrationMetrics {
	return &dingTalkIntegrationMetrics{
		accountBegin: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dingtalk_account_begin_total",
			Help: "Total DingTalk account binding begin attempts by outcome.",
		}, metricLabels("dingtalk_account_begin_total")),
		accountCallback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dingtalk_account_callback_total",
			Help: "Total DingTalk account binding callbacks by outcome.",
		}, metricLabels("dingtalk_account_callback_total")),
		accountSubscriptionVerify: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dingtalk_account_subscription_verify_total",
			Help: "Total DingTalk account subscription consistency checks by outcome.",
		}, metricLabels("dingtalk_account_subscription_verify_total")),
		accountUnbind: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dingtalk_account_unbind_total",
			Help: "Total DingTalk account unbind attempts by outcome.",
		}, metricLabels("dingtalk_account_unbind_total")),
		dispatchCredentialDerive: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dispatch_credential_derive_total",
			Help: "Total dispatch credential derivations by outcome and configured key id.",
		}, metricLabels("dispatch_credential_derive_total")),
		dispatchAuth: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dispatch_auth_total",
			Help: "Total inbound agent dispatch authentication attempts by outcome.",
		}, metricLabels("dispatch_auth_total")),
	}
}

func (m *dingTalkIntegrationMetrics) collectors() []prometheus.Collector {
	if m == nil {
		return nil
	}
	return []prometheus.Collector{
		m.accountBegin,
		m.accountCallback,
		m.accountSubscriptionVerify,
		m.accountUnbind,
		m.dispatchCredentialDerive,
		m.dispatchAuth,
	}
}

func (m *BusinessMetrics) RecordDingTalkAccountBegin(outcome string) {
	if m == nil || m.dingTalkIntegration == nil {
		return
	}
	m.dingTalkIntegration.accountBegin.WithLabelValues(NormalizeOperationOutcome(outcome)).Inc()
}

func (m *BusinessMetrics) RecordDingTalkAccountCallback(outcome string) {
	if m == nil || m.dingTalkIntegration == nil {
		return
	}
	m.dingTalkIntegration.accountCallback.WithLabelValues(NormalizeOperationOutcome(outcome)).Inc()
}

func (m *BusinessMetrics) RecordDingTalkAccountSubscriptionVerify(outcome string) {
	if m == nil || m.dingTalkIntegration == nil {
		return
	}
	m.dingTalkIntegration.accountSubscriptionVerify.WithLabelValues(NormalizeOperationOutcome(outcome)).Inc()
}

func (m *BusinessMetrics) RecordDingTalkAccountUnbind(outcome string) {
	if m == nil || m.dingTalkIntegration == nil {
		return
	}
	m.dingTalkIntegration.accountUnbind.WithLabelValues(NormalizeOperationOutcome(outcome)).Inc()
}

func (m *BusinessMetrics) RecordDispatchCredentialDerive(outcome, keyID string) {
	if m == nil || m.dingTalkIntegration == nil {
		return
	}
	m.dingTalkIntegration.dispatchCredentialDerive.WithLabelValues(
		NormalizeOperationOutcome(outcome),
		normalizeMetricKeyID(keyID),
	).Inc()
}

func (m *BusinessMetrics) RecordDispatchAuth(outcome string) {
	if m == nil || m.dingTalkIntegration == nil {
		return
	}
	m.dingTalkIntegration.dispatchAuth.WithLabelValues(NormalizeOperationOutcome(outcome)).Inc()
}
