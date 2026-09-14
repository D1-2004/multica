package dshhost

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func provisionSpec() ProvisionSpec {
	return ProvisionSpec{AccountID: "123", Region: "cn-beijing", Zone: "cn-beijing-k", TeamID: "team", FileSystemID: "fs", VPCID: "vpc", SecurityGroupID: "sg", VSwitchIDs: []string{"vsw"}, SizeLimit: 10, FileCountLimit: 10000}
}

type provisionMemory struct {
	p           Provision
	lostReceipt bool
	bound       bool
}

func (s *provisionMemory) BeginProvision(_ context.Context, k Key, spec ProvisionSpec) (Provision, error) {
	if s.p.Intent == uuid.Nil {
		s.p = Provision{Key: k, Spec: spec, Intent: uuid.New(), State: "planned", Resources: []string{}}
	}
	if s.p.Key != k || !reflect.DeepEqual(s.p.Spec, spec) {
		return Provision{}, ErrChanged
	}
	return s.p, nil
}
func (s *provisionMemory) ClaimProvisionStep(_ context.Context, p Provision) (Provision, error) {
	if p.Step != s.p.Step || s.p.State != "planned" {
		return Provision{}, ErrChanged
	}
	s.p.State = "creating"
	return s.p, nil
}
func (s *provisionMemory) CompleteProvisionStep(ctx context.Context, p Provision, id string) (Provision, error) {
	if err := ctx.Err(); err != nil {
		return Provision{}, err
	}
	if s.lostReceipt {
		s.lostReceipt = false
		return Provision{}, errors.New("database unavailable")
	}
	if s.p.Step != p.Step || s.p.State != "creating" {
		return Provision{}, ErrChanged
	}
	s.p.Resources = append(s.p.Resources, id)
	s.p.Step++
	s.p.State = "planned"
	return s.p, nil
}
func (s *provisionMemory) FinishProvision(_ context.Context, p Provision, storage Storage) (Host, error) {
	if err := validateProvisionStorage(p, storage); err != nil {
		return Host{}, err
	}
	s.bound = true
	s.p.State = "complete"
	return Host{Key: p.Key, Storage: storage, State: "offline"}, nil
}

type provisionCloud struct {
	mu              sync.Mutex
	ids             map[string]string
	creates, finds  int
	ambiguousStep   int
	hidden          bool
	badVerification bool
	onCreate        context.CancelFunc
}

func (c *provisionCloud) CreateStorageResource(_ context.Context, p Provision) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creates++
	if c.ids == nil {
		c.ids = map[string]string{}
	}
	id := fmt.Sprintf("resource-%s", p.StepIntent())
	c.ids[p.StepIntent()] = id
	if c.onCreate != nil {
		c.onCreate()
	}
	if p.Step == c.ambiguousStep {
		return "", errors.New("credential-bearing provider body must not escape")
	}
	return id, nil
}
func (c *provisionCloud) FindStorageResource(_ context.Context, p Provision) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finds++
	if c.hidden {
		return "", nil
	}
	return c.ids[p.StepIntent()], nil
}
func (c *provisionCloud) VerifyStorage(_ context.Context, p Provision) (Storage, error) {
	if c.badVerification {
		return Storage{}, errors.New("ownership mismatch")
	}
	return Storage{FileSystemID: p.Spec.FileSystemID, SpaceID: p.Resources[ProvisionSpace], AccessPointARN: p.Resources[ProvisionAccessPoint], RoleARN: p.Resources[ProvisionRole], VolumeName: p.Resources[ProvisionVolume], VPCID: p.Spec.VPCID, SecurityGroupID: p.Spec.SecurityGroupID, VSwitchIDs: p.Spec.VSwitchIDs}, nil
}

func TestProvisionUnknownCreateNeverRepeats(t *testing.T) {
	for step := range provisionStepCount {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			s := &provisionMemory{}
			c := &provisionCloud{ambiguousStep: step}
			m := Provisioner{s, c}
			k := Key{uuid.New(), uuid.New()}
			if _, err := m.Ensure(context.Background(), k, provisionSpec()); !errors.Is(err, ErrPending) {
				t.Fatal(err)
			}
			if s.bound || s.p.Step != step || s.p.State != "creating" {
				t.Fatalf("unexpected state: %+v", s.p)
			}
			c.hidden = true
			for range 3 {
				if _, err := m.Ensure(context.Background(), k, provisionSpec()); !errors.Is(err, ErrPending) {
					t.Fatal(err)
				}
			}
			if c.creates != step+1 {
				t.Fatalf("ambiguous create repeated: %d", c.creates)
			}
			c.hidden = false
			h, err := m.Ensure(context.Background(), k, provisionSpec())
			if err != nil {
				t.Fatal(err)
			}
			if !s.bound || h.Key != k || c.creates != provisionStepCount {
				t.Fatalf("binding or creates: %+v %d", h, c.creates)
			}
			if _, err = m.Ensure(context.Background(), k, provisionSpec()); err != nil {
				t.Fatal(err)
			}
			if c.creates != provisionStepCount {
				t.Fatal("completed provisioning recreated resources")
			}
		})
	}
}

func TestProvisionLostReceiptAndVerification(t *testing.T) {
	s := &provisionMemory{lostReceipt: true}
	c := &provisionCloud{ambiguousStep: -1}
	m := Provisioner{s, c}
	k := Key{uuid.New(), uuid.New()}
	if _, err := m.Ensure(context.Background(), k, provisionSpec()); err == nil {
		t.Fatal("expected lost database receipt")
	}
	if s.p.State != "creating" {
		t.Fatal("lost receipt reset the intent")
	}
	c.badVerification = true
	if _, err := m.Ensure(context.Background(), k, provisionSpec()); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if s.bound || c.creates != provisionStepCount || c.finds != 1 {
		t.Fatal("verification failure bound storage or recreated resources")
	}
	c.badVerification = false
	if _, err := m.Ensure(context.Background(), k, provisionSpec()); err != nil {
		t.Fatal(err)
	}
	changed := provisionSpec()
	changed.FileSystemID = "other"
	if _, err := m.Ensure(context.Background(), k, changed); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	if c.creates != provisionStepCount {
		t.Fatal("configuration change caused provisioning")
	}
}

func TestProvisionCancelledCallerPreservesReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &provisionMemory{}
	c := &provisionCloud{ambiguousStep: -1, onCreate: cancel}
	m := Provisioner{s, c}
	_, err := m.Ensure(ctx, Key{uuid.New(), uuid.New()}, provisionSpec())
	if !errors.Is(err, context.Canceled) || s.p.Step != 1 || s.bound || c.creates != 1 {
		t.Fatalf("receipt lost on request cancellation: %v %+v", err, s.p)
	}
}

func TestProvisionRejectsRedirectedStorage(t *testing.T) {
	p := Provision{Spec: provisionSpec(), Step: provisionStepCount, State: "planned", Resources: []string{"space", "ap", "role", "policy", "attachment", "volume"}}
	s, _ := (&provisionCloud{}).VerifyStorage(context.Background(), p)
	s.AccessPointARN = "other"
	if err := validateProvisionStorage(p, s); err == nil {
		t.Fatal("provider redirected the frozen AP")
	}
}

// Run only against an authorized preproduction database. Separate pools model
// different replicas; unit fixtures above do not prove these SQL transitions.
func TestProvisionPostgresCompetingReplicas(t *testing.T) {
	a, b := stores(t)
	k := Key{uuid.New(), uuid.New()}
	spec := provisionSpec()
	ctx := context.Background()
	var wg sync.WaitGroup
	intents := make(chan uuid.UUID, 24)
	errs := make(chan error, 24)
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := a
			if i%2 != 0 {
				s = b
			}
			p, err := s.BeginProvision(ctx, k, spec)
			if err != nil {
				errs <- err
				return
			}
			intents <- p.Intent
		}()
	}
	wg.Wait()
	close(errs)
	close(intents)
	for err := range errs {
		t.Fatal(err)
	}
	var intent uuid.UUID
	for id := range intents {
		if intent != uuid.Nil && intent != id {
			t.Fatal("multiple intents")
		}
		intent = id
	}
	p, err := a.BeginProvision(ctx, k, spec)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := a.ClaimProvisionStep(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.ClaimProvisionStep(ctx, p); !errors.Is(err, ErrChanged) {
		t.Fatal("second creator admitted", err)
	}
	c := &provisionCloud{ambiguousStep: -1}
	id, err := c.CreateStorageResource(ctx, claimed)
	if err != nil {
		t.Fatal(err)
	}
	// Discard the create response and resume on another replica.
	h, err := (Provisioner{b, c}).Ensure(ctx, k, spec)
	if err != nil {
		t.Fatal(err)
	}
	if h.SpaceID != id || c.creates != provisionStepCount || c.finds != 1 {
		t.Fatal("did not adopt original resources")
	}
	if _, err = a.CompleteProvisionStep(ctx, claimed, "stale"); !errors.Is(err, ErrChanged) {
		t.Fatal("stale receipt accepted", err)
	}
	spec.Region = "other"
	if _, err = b.BeginProvision(ctx, k, spec); !errors.Is(err, ErrChanged) {
		t.Fatal("placement changed", err)
	}
}
