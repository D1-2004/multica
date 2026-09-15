package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

var (
	asbRegionalAPIHost = regexp.MustCompile(`^((?:pre-|daily-)?)sandbox(?:-[a-z0-9-]+)?\.aone\.alibaba-inc\.com$`)
	asbRegionName      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+$`)
)

const ASBExecutionRegionsKey = "asb_regions"
const asbSandboxRegionKey = "multica.region"

// An absent or empty selection preserves automatic placement across API regions.
func ParseASBExecutionRegions(raw []byte) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("invalid runtime_config: %w", err)
	}
	value, present := config[ASBExecutionRegionsKey]
	if !present {
		return nil, nil
	}
	var regions []string
	if string(value) == "null" || json.Unmarshal(value, &regions) != nil {
		return nil, errors.New("asb_regions must be an array of region IDs")
	}
	if len(regions) > 32 {
		return nil, errors.New("asb_regions supports at most 32 regions")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(regions))
	for _, region := range regions {
		if len(region) > 48 || !asbRegionName.MatchString(region) {
			return nil, fmt.Errorf("invalid ASB region %q", region)
		}
		if !seen[region] {
			result = append(result, region)
			seen[region] = true
		}
	}
	sort.Strings(result)
	return result, nil
}

func asbRegionAllowed(region string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == region {
			return true
		}
	}
	return false
}

// ASB selects the region from the API hostname, not the create body. Only
// rewrite the known ASB service domain; custom gateways retain their routing.
// The copy is used for creation only. Inventory and tenant coordination keep
// using the original client, so all regions share the same lock and cooldown.
func (c *ASBClient) forCreateRegion(region string) (*ASBClient, error) {
	match := asbRegionalAPIHost.FindStringSubmatch(strings.ToLower(c.baseURL.Hostname()))
	if match == nil || region == "" {
		return c, nil
	}
	if len(region) > 48 || !asbRegionName.MatchString(region) {
		return nil, fmt.Errorf("invalid ASB quota region %q", region)
	}
	regional := *c
	baseURL := *c.baseURL
	baseURL.Host = match[1] + "sandbox-" + region + ".aone.alibaba-inc.com"
	if port := c.baseURL.Port(); port != "" {
		baseURL.Host = net.JoinHostPort(baseURL.Host, port)
	}
	regional.baseURL = &baseURL
	return &regional, nil
}

func isASBQuotaExceeded(err error) bool {
	var upstream *ASBHTTPError
	return errors.As(err, &upstream) && upstream.Operation == "create_sandbox" &&
		upstream.StatusCode == http.StatusForbidden && upstream.ErrorCode == "QUOTA_EXCEEDED"
}

func createASBSandboxInAvailableRegion(
	ctx context.Context,
	client *ASBClient,
	runtimeID pgtype.UUID,
	input ASBCreateSandboxInput,
	quotas []ASBSandboxQuota,
) (*ASBSandbox, error) {
	if len(input.AllowedRegions) > 0 {
		if asbRegionalAPIHost.FindStringSubmatch(strings.ToLower(client.baseURL.Hostname())) == nil {
			return nil, errors.New("ASB regional selection requires a regional ASB service endpoint")
		}
		selected := make([]ASBSandboxQuota, 0, len(quotas))
		for _, allocation := range quotas {
			if asbRegionAllowed(allocation.Region, input.AllowedRegions) {
				selected = append(selected, allocation)
			}
		}
		if len(selected) == 0 {
			return nil, errors.New("selected ASB regions are unavailable for this Runtime credential")
		}
		quotas = selected
	}
	if _, _, _, err := summarizeASBQuotas(quotas); err != nil {
		return nil, err
	}
	allocations := append([]ASBSandboxQuota(nil), quotas...)
	sort.SliceStable(allocations, func(i, j int) bool {
		left, right := allocations[i], allocations[j]
		if left.Quota-left.Usage != right.Quota-right.Usage {
			return left.Quota-left.Usage > right.Quota-right.Usage
		}
		return left.Region < right.Region
	})
	attempted := make(map[string]bool)
	var rejected error
	for _, allocation := range allocations {
		if allocation.Quota <= allocation.Usage {
			continue
		}
		regional, err := client.forCreateRegion(allocation.Region)
		if err != nil {
			return nil, err
		}
		// A custom gateway may expose several allocations through one host.
		// Never repeat the same request there for each regional quota row.
		if attempted[regional.baseURL.Host] {
			continue
		}
		attempted[regional.baseURL.Host] = true
		slog.Info("ASB sandbox create selected capacity", "event", "asb_capacity_region_selected",
			"runtime_id", util.UUIDToString(runtimeID), "network_zone", allocation.NetworkZone,
			"region", allocation.Region, "api_host", regional.baseURL.Host,
			"quota", allocation.Quota, "usage", allocation.Usage)
		regionalInput := input
		regionalInput.Metadata = make(map[string]string, len(input.Metadata)+1)
		for key, value := range input.Metadata {
			regionalInput.Metadata[key] = value
		}
		regionalInput.Metadata[asbSandboxRegionKey] = allocation.Region
		sandbox, err := regional.CreateSandbox(ctx, regionalInput)
		if err == nil {
			slog.Info("ASB sandbox created in selected region", "event", "asb_capacity_region_created",
				"runtime_id", util.UUIDToString(runtimeID), "sandbox_id", sandbox.ID,
				"region", allocation.Region, "api_host", regional.baseURL.Host)
			return sandbox, nil
		}
		logASBCreateFailure("regional_capacity", runtimeID, err)
		if !isASBQuotaExceeded(err) {
			// Authentication, policy, network and rate-limit failures are not
			// regional slot contention and must not trigger failover or reclaim.
			return nil, err
		}
		rejected = err
	}
	return nil, errors.Join(ErrASBCapacityUnavailable, rejected)
}
