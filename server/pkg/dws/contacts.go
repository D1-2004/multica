package dws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ContactService reads the address book.
type ContactService struct{ c *Client }

// Profile is a DingTalk identity.
type Profile struct {
	UserID  string `json:"userId"`
	Name    string `json:"name,omitempty"`
	CorpID  string `json:"corpId,omitempty"`
	OrgName string `json:"orgName,omitempty"`
}

// Me returns the identity behind the Client's token.
func (s *ContactService) Me(ctx context.Context) (Profile, error) {
	raw, err := s.c.Call(ctx, ServerContact, "get_current_user_profile", nil)
	if err != nil {
		return Profile{}, err
	}
	var rows []struct {
		Employee struct {
			UserID      string `json:"userId"`
			CorpID      string `json:"corpId"`
			OrgName     string `json:"orgName"`
			OrgUserName string `json:"orgUserName"`
		} `json:"orgEmployeeModel"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 || rows[0].Employee.UserID == "" {
		return Profile{}, errors.New("dws: unexpected get_current_user_profile result")
	}
	e := rows[0].Employee
	return Profile{UserID: e.UserID, Name: e.OrgUserName, CorpID: e.CorpID, OrgName: e.OrgName}, nil
}

// Person is a contact search hit. OpenDingTalkID is relative to the caller.
type Person struct {
	UserID         string `json:"userId,omitempty"`
	OpenDingTalkID string `json:"openDingTalkId,omitempty"`
	Name           string `json:"name,omitempty"`
	Nick           string `json:"nick,omitempty"`
	Title          string `json:"title,omitempty"`
}

// Search finds colleagues and friends by name, nickname or keyword.
func (s *ContactService) Search(ctx context.Context, keyword string) ([]Person, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, invalid("contact search needs a keyword")
	}
	raw, err := s.c.Call(ctx, ServerContact, "search_contact_by_key_word", map[string]any{"keyword": keyword})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		UserID         string `json:"userId"`
		OpenDingTalkID string `json:"openDingTalkId"`
		Name           string `json:"name"`
		Nick           string `json:"nick"`
		FlowerName     string `json:"flowerName"`
		Title          string `json:"title"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("dws: decode contact search: %w", err)
	}
	people := make([]Person, 0, len(rows))
	for _, r := range rows {
		nick := r.Nick
		if nick == "" {
			nick = r.FlowerName
		}
		people = append(people, Person{UserID: r.UserID, OpenDingTalkID: r.OpenDingTalkID, Name: r.Name, Nick: nick, Title: r.Title})
	}
	return people, nil
}
