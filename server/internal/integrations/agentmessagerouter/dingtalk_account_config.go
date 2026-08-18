package agentmessagerouter

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ChannelTypeDingTalkAccount     = "dingtalk_account"
	DingTalkMessageScopeDirectOnly = "direct_only"
	DingTalkMessageScopeCustom     = "custom"
	DingTalkMessageScopeAll        = "all"
	DingTalkSurfaceIssue           = "issue"
	DingTalkSurfaceChat            = "chat"
	DingTalkSurfaceAuto            = "auto"
	DingTalkBindingStatusFailed    = "failed"
	dingTalkAccountSchema          = 1
	callbackTokenDomain            = "dingtalk-account-callback:v1:"
	maxConversationCIDBytes        = 256
	maxConversationNameRunes       = 256
	maxConversationMediaIDBytes    = 1024
	maxBindingDomains              = 64
	// DingTalkMessageScopeVersionLegacy 是 v1 单维 cids 契约；存量记录缺版本字段，归一化为该值。
	DingTalkMessageScopeVersionLegacy = 1
	// DingTalkMessageScopeVersionBuckets 是 v2 双维度分桶契约（direct_cids/group_cids）。
	DingTalkMessageScopeVersionBuckets = 2
	maxScopeCIDBytes                   = 512
)

type DingTalkConversationSnapshot struct {
	CID           string `json:"cid"`
	Name          string `json:"name"`
	AvatarMediaID string `json:"avatar_media_id,omitempty"`
	AvatarURL     string `json:"avatar_url,omitempty"`
}

// DingTalkMessageScopeDetail 是 v2 双维度订阅范围明细快照，与 dm-bind 回调的
// message_scope_detail 契约一致：direct/group 桶接受 ["*"]（所有）、cid 列表（指定）或
// 空数组（不接收），两桶不允许同时为空。EmojiReactionCids 是表情回复事件监听桶（可选），
// 缺省/空数组表示不订阅；与消息桶不同，它禁通配符 "*"（事件中心要求限定会话），
// 且不参与"两桶不同时为空"的判定。
type DingTalkMessageScopeDetail struct {
	DirectCids        []string `json:"direct_cids"`
	GroupCids         []string `json:"group_cids"`
	EmojiReactionCids []string `json:"emoji_reaction_cids,omitempty"`
}

type DingTalkAccountConfig struct {
	SchemaVersion      int    `json:"schema_version"`
	DispatchEndpointID string `json:"dispatch_endpoint_id"`
	DispatchKeyID      string `json:"dispatch_key_id"`
	// DispatchURL is retained only for old binaries during a rolling rollout.
	// Current routing and ownership checks use DispatchEndpointID.
	DispatchURL          string                         `json:"dispatch_url"`
	CallbackTokenHash    string                         `json:"callback_token_hash,omitempty"`
	CallbackExpiresAt    time.Time                      `json:"callback_expires_at,omitempty"`
	RouterSourceID       string                         `json:"router_source_id,omitempty"`
	RouterPlatform       string                         `json:"router_platform,omitempty"`
	RouterTenantID       string                         `json:"router_tenant_id,omitempty"`
	RouterAccountID      string                         `json:"router_account_id,omitempty"`
	AccountDisplayName   string                         `json:"account_display_name,omitempty"`
	AccountAvatarURL     string                         `json:"account_avatar_url,omitempty"`
	SurfaceType          string                         `json:"surface_type,omitempty"`
	MessageRouteStatus   string                         `json:"message_route_status,omitempty"`
	MessageRouteError    *BindingTaskError              `json:"message_route_error,omitempty"`
	MessageScope         string                         `json:"message_scope"`
	MessageScopeVersion  int                            `json:"message_scope_version,omitempty"`
	MessageScopeDetail   *DingTalkMessageScopeDetail    `json:"message_scope_detail,omitempty"`
	EnabledDomains       []string                       `json:"enabled_domains,omitempty"`
	CalendarStartEnabled bool                           `json:"calendar_start_enabled,omitempty"`
	Conversations        []DingTalkConversationSnapshot `json:"conversations,omitempty"`
	// EmojiConversations 是表情回复监听会话快照，与 emoji_reaction_cids 一一对应，
	// 结构同 Conversations；identity 模式（message skipped）时为空。
	EmojiConversations []DingTalkConversationSnapshot `json:"emoji_conversations,omitempty"`
	BoundAt            *time.Time                     `json:"bound_at,omitempty"`
}

type PublicDingTalkAccountBinding struct {
	ID           string                       `json:"id"`
	WorkspaceID  string                       `json:"workspace_id"`
	AgentID      string                       `json:"agent_id"`
	DWSIdentity  PublicDingTalkBindingOutcome `json:"dws_identity"`
	MessageRoute PublicDingTalkBindingOutcome `json:"message_route"`
}

type PublicDingTalkBindingOutcome struct {
	Status               string                                `json:"status"`
	Source               string                                `json:"source,omitempty"`
	OrganizationName     string                                `json:"organization_name,omitempty"`
	AccountDisplayName   string                                `json:"account_display_name,omitempty"`
	AccountAvatarURL     string                                `json:"account_avatar_url,omitempty"`
	SurfaceType          string                                `json:"surface_type,omitempty"`
	MessageScope         string                                `json:"message_scope,omitempty"`
	MessageScopeVersion  int                                   `json:"message_scope_version,omitempty"`
	Subscription         *PublicDingTalkMessageScopeView       `json:"subscription,omitempty"`
	LegacyView           *PublicDingTalkMessageScopeLegacyView `json:"legacy_view,omitempty"`
	EnabledDomains       []string                              `json:"enabled_domains,omitempty"`
	CalendarStartEnabled bool                                  `json:"calendar_start_enabled,omitempty"`
	Conversations        []DingTalkConversationSnapshot        `json:"conversations,omitempty"`
	EmojiConversations   []DingTalkConversationSnapshot        `json:"emoji_conversations,omitempty"`
	BoundAt              *time.Time                            `json:"bound_at,omitempty"`
	Error                *BindingTaskError                     `json:"error,omitempty"`
}

// PublicDingTalkMessageScopeView 是绑定查询接口返回的双维度新视图（dm-bind 方案 §2.4
// subscription）；v1 记录按升格规则生成，v2 记录取存储明细。
type PublicDingTalkMessageScopeView struct {
	DirectCids []string `json:"direct_cids"`
	GroupCids  []string `json:"group_cids"`
	// EmojiReactionCids 表情回复监听范围；v1 记录升格为空数组。
	EmojiReactionCids []string `json:"emoji_reaction_cids"`
}

// PublicDingTalkMessageScopeLegacyView 是老视图兜底（dm-bind 方案 §2.4 legacyView），
// 保证未升级的渲染方不炸；Cids 为 null 表示该 v2 组合老契约不可表达。
type PublicDingTalkMessageScopeLegacyView struct {
	MessageScope string   `json:"message_scope"`
	Cids         []string `json:"cids"`
}

func NewPendingDingTalkAccountConfig(endpointID, dispatchURL, callbackHash string, callbackExpiresAt time.Time) DingTalkAccountConfig {
	keyID, _, _ := stringsCutEndpointID(endpointID)
	return DingTalkAccountConfig{
		SchemaVersion:      dingTalkAccountSchema,
		DispatchEndpointID: endpointID,
		DispatchKeyID:      keyID,
		DispatchURL:        dispatchURL,
		CallbackTokenHash:  callbackHash,
		CallbackExpiresAt:  callbackExpiresAt.UTC(),
		MessageScope:       DingTalkMessageScopeDirectOnly,
	}
}

func (c DingTalkAccountConfig) Marshal() ([]byte, error) {
	if c.MessageScopeVersion == 0 {
		c.MessageScopeVersion = DingTalkMessageScopeVersionLegacy
	}
	domains, err := normalizeBindingDomains(c.bindingDomains())
	if err != nil {
		return nil, err
	}
	c.EnabledDomains = domains
	if len(domains) > 0 {
		c.CalendarStartEnabled = containsBindingDomain(domains, "calendar")
	}
	messageScope, conversations, err := normalizeDingTalkConversationBindingForVersion(c.MessageScope, c.Conversations, c.MessageScopeVersion)
	if err != nil {
		return nil, err
	}
	c.MessageScope = messageScope
	c.Conversations = conversations
	emojiConversations, err := normalizeDingTalkConversationSnapshots(c.EmojiConversations)
	if err != nil {
		return nil, err
	}
	c.EmojiConversations = emojiConversations
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

func ParseDingTalkAccountConfig(raw []byte) (DingTalkAccountConfig, error) {
	var config DingTalkAccountConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return DingTalkAccountConfig{}, fmt.Errorf("decode dingtalk account config: %w", err)
	}
	if config.MessageScope == "" {
		config.MessageScope = DingTalkMessageScopeDirectOnly
	}
	if config.MessageScopeVersion == 0 {
		config.MessageScopeVersion = DingTalkMessageScopeVersionLegacy
	}
	domains, err := normalizeBindingDomains(config.bindingDomains())
	if err != nil {
		return DingTalkAccountConfig{}, err
	}
	config.EnabledDomains = domains
	if len(domains) > 0 {
		config.CalendarStartEnabled = containsBindingDomain(domains, "calendar")
	}
	messageScope, conversations, err := normalizeDingTalkConversationBindingForVersion(config.MessageScope, config.Conversations, config.MessageScopeVersion)
	if err != nil {
		return DingTalkAccountConfig{}, err
	}
	config.MessageScope = messageScope
	config.Conversations = conversations
	emojiConversations, err := normalizeDingTalkConversationSnapshots(config.EmojiConversations)
	if err != nil {
		return DingTalkAccountConfig{}, err
	}
	config.EmojiConversations = emojiConversations
	if err := config.Validate(); err != nil {
		return DingTalkAccountConfig{}, err
	}
	return config, nil
}

func (c DingTalkAccountConfig) Validate() error {
	if c.SchemaVersion != dingTalkAccountSchema {
		return errors.New("unsupported dingtalk account config schema")
	}
	keyID, err := parseEndpointID(c.DispatchEndpointID)
	if err != nil || keyID != c.DispatchKeyID {
		return errors.New("dingtalk account dispatch endpoint is invalid")
	}
	if (c.CallbackTokenHash == "") != c.CallbackExpiresAt.IsZero() {
		return errors.New("dingtalk account callback credential is invalid")
	}
	if c.CallbackTokenHash != "" {
		decoded, err := hex.DecodeString(c.CallbackTokenHash)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("dingtalk account callback credential is invalid")
		}
	}
	accountKeyFields := 0
	if c.RouterPlatform != "" {
		accountKeyFields++
	}
	if c.RouterTenantID != "" {
		accountKeyFields++
	}
	if c.RouterAccountID != "" {
		accountKeyFields++
	}
	if accountKeyFields != 0 && accountKeyFields != 3 {
		return errors.New("dingtalk account router account key is incomplete")
	}
	if accountKeyFields == 3 && (c.RouterPlatform != "dingtalk" ||
		!validRouterIdentifier(c.RouterTenantID) || !validRouterIdentifier(c.RouterAccountID)) {
		return errors.New("dingtalk account router account key is invalid")
	}
	if c.RouterSourceID != "" && c.BoundAt == nil {
		return errors.New("dingtalk account bound time is required")
	}
	domains, err := normalizeBindingDomains(c.EnabledDomains)
	if err != nil {
		return err
	}
	if c.RouterSourceID != "" && (len(domains) == 0 || !containsBindingDomain(domains, "channel")) {
		return errors.New("dingtalk account enabled domains are invalid")
	}
	if c.SurfaceType != "" && !validDingTalkSurfaceType(c.SurfaceType) {
		return errors.New("dingtalk account surface type is invalid")
	}
	if utf8.RuneCountInString(strings.TrimSpace(c.AccountDisplayName)) > maxAccountNameRunes ||
		!validAccountAvatarURL(strings.TrimSpace(c.AccountAvatarURL)) {
		return errors.New("dingtalk account snapshot is invalid")
	}
	if c.MessageRouteStatus != "" &&
		c.MessageRouteStatus != DingTalkBindingStatusFailed {
		return errors.New("dingtalk message result status is invalid")
	}
	if c.MessageRouteError != nil &&
		(c.MessageRouteStatus != DingTalkBindingStatusFailed ||
			!validBindingTaskError(c.MessageRouteError)) {
		return errors.New("dingtalk message result error is invalid")
	}
	scopeVersion := c.MessageScopeVersion
	if scopeVersion == 0 {
		scopeVersion = DingTalkMessageScopeVersionLegacy
	}
	if scopeVersion != DingTalkMessageScopeVersionLegacy && scopeVersion != DingTalkMessageScopeVersionBuckets {
		return errors.New("dingtalk account message scope version is invalid")
	}
	if scopeVersion == DingTalkMessageScopeVersionLegacy && c.MessageScopeDetail != nil {
		return errors.New("dingtalk account message scope detail is unexpected")
	}
	if scopeVersion == DingTalkMessageScopeVersionBuckets {
		if _, err := normalizeDingTalkMessageScopeDetail(c.MessageScopeDetail); err != nil {
			return err
		}
	}
	if _, _, err := normalizeDingTalkConversationBindingForVersion(c.MessageScope, c.Conversations, scopeVersion); err != nil {
		return err
	}
	if _, err := normalizeDingTalkConversationSnapshots(c.EmojiConversations); err != nil {
		return err
	}
	return nil
}

func (c DingTalkAccountConfig) bindingDomains() []string {
	return append([]string(nil), c.EnabledDomains...)
}

func normalizeBindingDomains(domains []string) ([]string, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	if len(domains) > maxBindingDomains {
		return nil, errors.New("dingtalk account enabled domains are invalid")
	}
	normalized := make([]string, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for index, domain := range domains {
		if !validRouterIdentifier(domain) {
			return nil, errors.New("dingtalk account enabled domains are invalid")
		}
		if _, exists := seen[domain]; exists {
			return nil, errors.New("dingtalk account enabled domains are duplicated")
		}
		seen[domain] = struct{}{}
		normalized[index] = domain
	}
	return normalized, nil
}

func containsBindingDomain(domains []string, expected string) bool {
	for _, domain := range domains {
		if domain == expected {
			return true
		}
	}
	return false
}

func normalizeDingTalkConversationBinding(messageScope string, conversations []DingTalkConversationSnapshot) (string, []DingTalkConversationSnapshot, error) {
	return normalizeDingTalkConversationBindingForVersion(messageScope, conversations, DingTalkMessageScopeVersionLegacy)
}

// normalizeDingTalkConversationBindingForVersion 在 v2 契约下放宽“custom 必须带会话列表”：
// v2 的 message_scope 只是给老渲染方的兜底合成值，指定明细以 message_scope_detail 为准，
// 存在“custom 但没有任何指定会话”（一维通配、另一维不接收）的合法组合。
func normalizeDingTalkConversationBindingForVersion(messageScope string, conversations []DingTalkConversationSnapshot, scopeVersion int) (string, []DingTalkConversationSnapshot, error) {
	if messageScope == "" {
		messageScope = DingTalkMessageScopeDirectOnly
	}
	switch messageScope {
	case DingTalkMessageScopeDirectOnly, DingTalkMessageScopeCustom, DingTalkMessageScopeAll:
	default:
		return "", nil, errors.New("dingtalk account message scope is invalid")
	}
	if messageScope == DingTalkMessageScopeCustom && len(conversations) == 0 && scopeVersion != DingTalkMessageScopeVersionBuckets {
		return "", nil, errors.New("custom dingtalk account message scope requires conversations")
	}

	normalized, err := normalizeDingTalkConversationSnapshots(conversations)
	if err != nil {
		return "", nil, err
	}
	return messageScope, normalized, nil
}

// normalizeDingTalkConversationSnapshots 校验并归一化会话快照列表（trim、长度/字符集、
// cid 去重），消息监听 conversations 与表情回复 emoji_conversations 共用。
func normalizeDingTalkConversationSnapshots(conversations []DingTalkConversationSnapshot) ([]DingTalkConversationSnapshot, error) {
	normalized := make([]DingTalkConversationSnapshot, len(conversations))
	seenCIDs := make(map[string]struct{}, len(conversations))
	for i, conversation := range conversations {
		conversation.CID = strings.TrimSpace(conversation.CID)
		conversation.Name = strings.TrimSpace(conversation.Name)
		conversation.AvatarMediaID = strings.TrimSpace(conversation.AvatarMediaID)
		conversation.AvatarURL = strings.TrimSpace(conversation.AvatarURL)
		if conversation.CID == "" || len(conversation.CID) > maxConversationCIDBytes ||
			!utf8.ValidString(conversation.CID) || strings.IndexFunc(conversation.CID, unicode.IsControl) >= 0 {
			return nil, errors.New("dingtalk conversation cid is invalid")
		}
		if conversation.Name == "" || utf8.RuneCountInString(conversation.Name) > maxConversationNameRunes ||
			!utf8.ValidString(conversation.Name) || strings.IndexFunc(conversation.Name, unicode.IsControl) >= 0 {
			return nil, errors.New("dingtalk conversation name is invalid")
		}
		if len(conversation.AvatarMediaID) > maxConversationMediaIDBytes ||
			!utf8.ValidString(conversation.AvatarMediaID) || strings.IndexFunc(conversation.AvatarMediaID, unicode.IsControl) >= 0 {
			return nil, errors.New("dingtalk conversation avatar media id is invalid")
		}
		if !validAccountAvatarURL(conversation.AvatarURL) {
			return nil, errors.New("dingtalk conversation avatar url is invalid")
		}
		if _, exists := seenCIDs[conversation.CID]; exists {
			return nil, errors.New("dingtalk conversation cid is duplicated")
		}
		seenCIDs[conversation.CID] = struct{}{}
		normalized[i] = conversation
	}
	return normalized, nil
}

// normalizeDingTalkMessageScopeDetail 校验 v2 双维度明细（与 dm-bind 页面校验对齐）：
// direct/group 两字段必传（允许空数组）、不允许同时为空、"*" 桶内独占、cid 非空且 ≤512
// 无空白字符。emoji 桶可选：缺省归一化为空数组，禁通配符 "*"，元素规则与消息桶一致。
func normalizeDingTalkMessageScopeDetail(detail *DingTalkMessageScopeDetail) (*DingTalkMessageScopeDetail, error) {
	if detail == nil || detail.DirectCids == nil || detail.GroupCids == nil {
		return nil, errors.New("dingtalk message scope detail is incomplete")
	}
	if len(detail.DirectCids) == 0 && len(detail.GroupCids) == 0 {
		return nil, errors.New("dingtalk message scope detail is empty")
	}
	directCids, err := normalizeScopeCIDBucket(detail.DirectCids)
	if err != nil {
		return nil, err
	}
	groupCids, err := normalizeScopeCIDBucket(detail.GroupCids)
	if err != nil {
		return nil, err
	}
	emojiReactionCids, err := normalizeEmojiReactionCIDBucket(detail.EmojiReactionCids)
	if err != nil {
		return nil, err
	}
	return &DingTalkMessageScopeDetail{
		DirectCids:        directCids,
		GroupCids:         groupCids,
		EmojiReactionCids: emojiReactionCids,
	}, nil
}

// normalizeEmojiReactionCIDBucket 校验表情回复监听桶：可选（nil 归一化为空数组），
// 禁通配符 "*"（事件中心要求 emotion_reply 订阅限定会话），元素规则与消息桶一致。
func normalizeEmojiReactionCIDBucket(cids []string) ([]string, error) {
	normalized := make([]string, 0, len(cids))
	for _, cid := range cids {
		if cid == "*" || !validScopeCID(cid) {
			return nil, errors.New("dingtalk emoji reaction scope cid is invalid")
		}
		normalized = append(normalized, cid)
	}
	return normalized, nil
}

func normalizeScopeCIDBucket(cids []string) ([]string, error) {
	normalized := make([]string, len(cids))
	for index, cid := range cids {
		if cid == "*" {
			if len(cids) != 1 {
				return nil, errors.New("dingtalk message scope wildcard is not exclusive")
			}
			normalized[index] = cid
			continue
		}
		if !validScopeCID(cid) {
			return nil, errors.New("dingtalk message scope cid is invalid")
		}
		normalized[index] = cid
	}
	return normalized, nil
}

func validScopeCID(cid string) bool {
	return cid != "" && len(cid) <= maxScopeCIDBytes && utf8.ValidString(cid) &&
		strings.IndexFunc(cid, unicode.IsSpace) < 0
}

func copyScopeCids(cids []string) []string {
	copied := make([]string, len(cids))
	copy(copied, cids)
	return copied
}

// upgradeLegacyMessageScopeView 把 v1 订阅范围升格为双维度新视图。direct_only 底层 cid 为
// "uid:uid"（"我聊"，自己给自己发消息），事件中心不具备该场景投递能力，按行为等效升格为
// 两个维度均不接收。
func upgradeLegacyMessageScopeView(scope string, conversations []DingTalkConversationSnapshot) PublicDingTalkMessageScopeView {
	switch scope {
	case DingTalkMessageScopeAll:
		return PublicDingTalkMessageScopeView{DirectCids: []string{"*"}, GroupCids: []string{"*"}, EmojiReactionCids: []string{}}
	case DingTalkMessageScopeCustom:
		view := PublicDingTalkMessageScopeView{
			DirectCids:        make([]string, 0, len(conversations)),
			GroupCids:         make([]string, 0, len(conversations)),
			EmojiReactionCids: []string{},
		}
		for _, conversation := range conversations {
			if strings.Contains(conversation.CID, ":") {
				view.DirectCids = append(view.DirectCids, conversation.CID)
			} else {
				view.GroupCids = append(view.GroupCids, conversation.CID)
			}
		}
		return view
	default:
		return PublicDingTalkMessageScopeView{DirectCids: []string{}, GroupCids: []string{}, EmojiReactionCids: []string{}}
	}
}

// legacyMessageScopeView 为 v1 记录生成老视图：message_scope 照抄存储值，cids 由存储的
// scope/conversations/accountID 还原（all → ["*"]，direct_only → ["uid:uid"]，custom → 会话 cid 列表）。
func legacyMessageScopeView(scope string, conversations []DingTalkConversationSnapshot, accountID string) PublicDingTalkMessageScopeLegacyView {
	view := PublicDingTalkMessageScopeLegacyView{MessageScope: scope}
	switch scope {
	case DingTalkMessageScopeAll:
		view.Cids = []string{"*"}
	case DingTalkMessageScopeCustom:
		cids := make([]string, 0, len(conversations))
		for _, conversation := range conversations {
			cids = append(cids, conversation.CID)
		}
		view.Cids = cids
	default:
		if accountID != "" {
			view.Cids = []string{accountID + ":" + accountID}
		}
	}
	return view
}

// downgradeBucketMessageScopeView 按 dm-bind 方案 §3 把 v2 双维度明细降级合成为老视图。
func downgradeBucketMessageScopeView(detail DingTalkMessageScopeDetail, accountID string) PublicDingTalkMessageScopeLegacyView {
	directAll := len(detail.DirectCids) == 1 && detail.DirectCids[0] == "*"
	groupAll := len(detail.GroupCids) == 1 && detail.GroupCids[0] == "*"
	switch {
	case directAll && groupAll:
		return PublicDingTalkMessageScopeLegacyView{MessageScope: DingTalkMessageScopeAll, Cids: []string{"*"}}
	case directAll && len(detail.GroupCids) == 0:
		view := PublicDingTalkMessageScopeLegacyView{MessageScope: DingTalkMessageScopeDirectOnly}
		if accountID != "" {
			view.Cids = []string{accountID + ":" + accountID}
		}
		return view
	case directAll || groupAll:
		// 一维通配另一维非通配：老契约不可表达，cids 置 null。
		return PublicDingTalkMessageScopeLegacyView{MessageScope: DingTalkMessageScopeCustom}
	default:
		cids := make([]string, 0, len(detail.DirectCids)+len(detail.GroupCids))
		cids = append(cids, detail.DirectCids...)
		cids = append(cids, detail.GroupCids...)
		return PublicDingTalkMessageScopeLegacyView{MessageScope: DingTalkMessageScopeCustom, Cids: cids}
	}
}

func (c DingTalkAccountConfig) PublicBinding(
	workspaceID,
	agentID,
	messageRouteStatus string,
	dwsIdentity PublicDingTalkBindingOutcome,
) PublicDingTalkAccountBinding {
	if c.MessageRouteStatus != "" {
		messageRouteStatus = c.MessageRouteStatus
	}
	scopeVersion := c.MessageScopeVersion
	if scopeVersion == 0 {
		scopeVersion = DingTalkMessageScopeVersionLegacy
	}
	subscription := upgradeLegacyMessageScopeView(c.MessageScope, c.Conversations)
	legacyView := legacyMessageScopeView(c.MessageScope, c.Conversations, c.RouterAccountID)
	if scopeVersion == DingTalkMessageScopeVersionBuckets && c.MessageScopeDetail != nil {
		subscription = PublicDingTalkMessageScopeView{
			DirectCids:        copyScopeCids(c.MessageScopeDetail.DirectCids),
			GroupCids:         copyScopeCids(c.MessageScopeDetail.GroupCids),
			EmojiReactionCids: copyScopeCids(c.MessageScopeDetail.EmojiReactionCids),
		}
		legacyView = downgradeBucketMessageScopeView(*c.MessageScopeDetail, c.RouterAccountID)
	}
	return PublicDingTalkAccountBinding{
		ID:          agentID,
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		DWSIdentity: dwsIdentity,
		MessageRoute: PublicDingTalkBindingOutcome{
			Status:               messageRouteStatus,
			AccountDisplayName:   c.AccountDisplayName,
			AccountAvatarURL:     c.AccountAvatarURL,
			SurfaceType:          c.SurfaceType,
			MessageScope:         c.MessageScope,
			MessageScopeVersion:  scopeVersion,
			Subscription:         &subscription,
			LegacyView:           &legacyView,
			EnabledDomains:       append([]string(nil), c.EnabledDomains...),
			CalendarStartEnabled: c.CalendarStartEnabled,
			Conversations:        append([]DingTalkConversationSnapshot(nil), c.Conversations...),
			EmojiConversations:   append([]DingTalkConversationSnapshot(nil), c.EmojiConversations...),
			BoundAt:              c.BoundAt,
			Error:                c.MessageRouteError,
		},
	}
}

func validDingTalkSurfaceType(surfaceType string) bool {
	return surfaceType == DingTalkSurfaceIssue ||
		surfaceType == DingTalkSurfaceChat ||
		surfaceType == DingTalkSurfaceAuto
}

func GenerateCallbackToken(random io.Reader) (string, string, error) {
	if random == nil {
		return "", "", errors.New("callback token random source is required")
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", "", fmt.Errorf("generate callback token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(value)
	return raw, HashCallbackToken(raw), nil
}

func HashCallbackToken(raw string) string {
	if !isCanonicalCallbackToken(raw) {
		return ""
	}
	sum := sha256.Sum256([]byte(callbackTokenDomain + raw))
	return hex.EncodeToString(sum[:])
}

func VerifyCallbackToken(raw, expectedHash string) bool {
	actual := HashCallbackToken(raw)
	if actual == "" || len(actual) != len(expectedHash) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expectedHash)) == 1
}

func isCanonicalCallbackToken(raw string) bool {
	if len(raw) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == raw
}

func stringsCutEndpointID(endpointID string) (string, string, bool) {
	for i := 0; i < len(endpointID); i++ {
		if endpointID[i] == '_' {
			return endpointID[:i], endpointID[i+1:], true
		}
	}
	return "", "", false
}
