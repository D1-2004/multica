package orgemphsf

import (
	"bytes"
	"context"
	"crypto/des"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	dapr "github.com/dapr/go-sdk/client"
)

const (
	defaultBindingName  = "hsf.consumer"
	defaultDaprGRPCPort = "50001"
	defaultTimeout      = 10 * time.Second

	serviceInterface = "com.dingtalk.org.service.OrgEmpService"
	serviceVersion   = "1.0.0"
	serviceGroup     = "HSF"
	methodName       = "getEmpInfoByStaffId"
	parameterTypes   = "java.lang.Long,java.lang.String"

	corpIDPrefix    = "ding"
	corpIDDESKey    = "369f6228"
	maxCorpIDBytes  = 256
	maxStaffIDBytes = 256
)

type Employee struct {
	UID     string
	OrgID   string
	StaffID string
}

type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid organization employee field: %s", e.Field)
}

type ServiceError struct {
	Code string
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("organization employee HSF request failed: %s", e.Code)
}

type bindingInvoker interface {
	InvokeBinding(context.Context, *dapr.InvokeBindingRequest) (*dapr.BindingEvent, error)
	Close()
}

type dialBinding func(context.Context, string) (bindingInvoker, error)

type Client struct {
	address     string
	bindingName string
	appName     string
	timeout     time.Duration
	dial        dialBinding
}

func NewClient() *Client {
	return &Client{
		address:     daprAddressFromEnv(),
		bindingName: defaultBindingName,
		appName:     strings.TrimSpace(os.Getenv("APP_NAME")),
		timeout:     defaultTimeout,
		dial: func(ctx context.Context, address string) (bindingInvoker, error) {
			return dapr.NewClientWithAddressContext(ctx, address)
		},
	}
}

func daprAddressFromEnv() string {
	if endpoint := strings.TrimSpace(os.Getenv("DAPR_GRPC_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	port := strings.TrimSpace(os.Getenv("DAPR_GRPC_PORT"))
	if port == "" {
		port = defaultDaprGRPCPort
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// ResolveEmployeeByCorpID resolves the employee behind one DingTalk bot
// message. The org ID is encoded in senderCorpId, so only the employee lookup
// crosses HSF and the task never reuses an Agent-bound identity.
func (c *Client) ResolveEmployeeByCorpID(ctx context.Context, corpID, staffID string) (Employee, error) {
	corpID = strings.TrimSpace(corpID)
	staffID = strings.TrimSpace(staffID)
	if corpID == "" || len(corpID) > maxCorpIDBytes {
		return Employee{}, &ValidationError{Field: "corp_id"}
	}
	if staffID == "" || len(staffID) > maxStaffIDBytes {
		return Employee{}, &ValidationError{Field: "staff_id"}
	}
	orgID, err := DecodeCorpID(corpID)
	if err != nil {
		return Employee{}, &ValidationError{Field: "corp_id"}
	}
	employee, err := c.GetEmployeeByStaffID(ctx, orgID, staffID)
	if err != nil {
		return Employee{}, err
	}
	if employee.OrgID != orgID || employee.StaffID != staffID {
		return Employee{}, errors.New("organization employee HSF returned a mismatched identity")
	}
	return employee, nil
}

// DecodeCorpID mirrors CorpIdUtils#getOrgIdByCorpId: strip the ding prefix,
// URL-decode the base16 payload, decrypt DES/ECB/PKCS5Padding, then parse the
// positive Java Long value.
func DecodeCorpID(corpID string) (string, error) {
	corpID = strings.TrimSpace(corpID)
	if !strings.HasPrefix(corpID, corpIDPrefix) || len(corpID) > maxCorpIDBytes {
		return "", errors.New("invalid corp ID")
	}
	encoded, err := url.QueryUnescape(strings.TrimPrefix(corpID, corpIDPrefix))
	if err != nil || encoded == "" {
		return "", errors.New("invalid corp ID encoding")
	}
	ciphertext, err := hex.DecodeString(encoded)
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%des.BlockSize != 0 {
		return "", errors.New("invalid corp ID ciphertext")
	}
	block, err := des.NewCipher([]byte(corpIDDESKey))
	if err != nil {
		return "", errors.New("initialize corp ID decoder")
	}
	plaintext := make([]byte, len(ciphertext))
	for offset := 0; offset < len(ciphertext); offset += des.BlockSize {
		block.Decrypt(plaintext[offset:offset+des.BlockSize], ciphertext[offset:offset+des.BlockSize])
	}
	plaintext, err = unpadPKCS5(plaintext)
	if err != nil {
		return "", errors.New("invalid corp ID padding")
	}
	orgID, err := strconv.ParseInt(string(plaintext), 10, 64)
	if err != nil || orgID <= 0 {
		return "", errors.New("invalid corp ID organization")
	}
	return strconv.FormatInt(orgID, 10), nil
}

func unpadPKCS5(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%des.BlockSize != 0 {
		return nil, errors.New("invalid padded data")
	}
	padding := int(data[len(data)-1])
	if padding < 1 || padding > des.BlockSize || padding > len(data) {
		return nil, errors.New("invalid padding size")
	}
	for _, value := range data[len(data)-padding:] {
		if int(value) != padding {
			return nil, errors.New("invalid padding bytes")
		}
	}
	return data[:len(data)-padding], nil
}

func (c *Client) GetEmployeeByStaffID(ctx context.Context, orgID, staffID string) (Employee, error) {
	orgID = strings.TrimSpace(orgID)
	staffID = strings.TrimSpace(staffID)
	if !isDecimalIdentifier(orgID) {
		return Employee{}, &ValidationError{Field: "org_id"}
	}
	if staffID == "" || len(staffID) > maxStaffIDBytes {
		return Employee{}, &ValidationError{Field: "staff_id"}
	}

	payload, err := json.Marshal([]any{json.Number(orgID), staffID})
	if err != nil {
		return Employee{}, errors.New("encode organization employee HSF request")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	invoker, err := c.dial(timeoutCtx, c.address)
	if err != nil {
		return Employee{}, fmt.Errorf("connect to Dapr sidecar: %w", err)
	}
	defer invoker.Close()

	metadata := map[string]string{
		"rpc-interface-name":         serviceInterface,
		"rpc-version":                serviceVersion,
		"rpc-group":                  serviceGroup,
		"rpc-method-name":            methodName,
		"rpc-method-parameter-types": parameterTypes,
		"serialization-type":         "application/json",
		"rpc-generic":                "true",
		"rpc-timeout":                strconv.FormatInt(c.timeout.Milliseconds(), 10),
	}
	if c.appName != "" {
		metadata["appName"] = c.appName
	}
	event, err := invoker.InvokeBinding(timeoutCtx, &dapr.InvokeBindingRequest{
		Name:      c.bindingName,
		Operation: "invoke",
		Data:      payload,
		Metadata:  metadata,
	})
	if err != nil {
		return Employee{}, fmt.Errorf("invoke organization employee HSF binding: %w", err)
	}
	if event == nil {
		return Employee{}, errors.New("organization employee HSF returned no response")
	}

	var response employeeResponse
	decoder := json.NewDecoder(bytes.NewReader(event.Data))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return Employee{}, errors.New("decode organization employee HSF response")
	}
	if !response.Success {
		return Employee{}, &ServiceError{Code: safeErrorCode(response.ErrorCode)}
	}
	employee := Employee{
		UID:     response.Result.UID.String(),
		OrgID:   response.Result.OrgID.String(),
		StaffID: strings.TrimSpace(response.Result.StaffID),
	}
	if !isDecimalIdentifier(employee.UID) || !isDecimalIdentifier(employee.OrgID) ||
		employee.StaffID == "" || len(employee.StaffID) > maxStaffIDBytes {
		return Employee{}, errors.New("organization employee HSF returned an incomplete response")
	}
	return employee, nil
}

type decimalIdentifier string

func (d *decimalIdentifier) UnmarshalJSON(raw []byte) error {
	var value string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	} else {
		value = string(raw)
	}
	value = strings.TrimSpace(value)
	if !isDecimalIdentifier(value) {
		return errors.New("invalid decimal identifier")
	}
	*d = decimalIdentifier(value)
	return nil
}

func (d decimalIdentifier) String() string {
	return string(d)
}

type employeeResponse struct {
	Success bool `json:"success"`
	Result  struct {
		UID     decimalIdentifier `json:"uid"`
		OrgID   decimalIdentifier `json:"orgId"`
		StaffID string            `json:"staffId"`
	} `json:"result"`
	ErrorCode string `json:"errorCode"`
}

func isDecimalIdentifier(value string) bool {
	if value == "" || value == "0" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func safeErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "UNKNOWN"
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "UNKNOWN"
		}
	}
	return code
}
