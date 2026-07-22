package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/version"
)

const DefaultTokenHeader = "xai-grok-cli"

type userInfo struct {
	UserID                    string    `json:"userId"`
	Email                     *string   `json:"email"`
	FirstName                 *string   `json:"firstName"`
	LastName                  *string   `json:"lastName"`
	ProfileImageAssetID       *string   `json:"profileImageAssetId"`
	PrincipalType             *string   `json:"principalType"`
	PrincipalID               *string   `json:"principalId"`
	TeamID                    *string   `json:"teamId"`
	TeamName                  *string   `json:"teamName"`
	TeamRole                  *string   `json:"teamRole"`
	OrganizationID            *string   `json:"organizationId"`
	OrganizationName          *string   `json:"organizationName"`
	OrganizationRole          *string   `json:"organizationRole"`
	UserBlockedReason         *string   `json:"userBlockedReason"`
	TeamBlockedReasons        *[]string `json:"teamBlockedReasons"`
	CodingDataRetentionOptOut *bool     `json:"codingDataRetentionOptOut"`
}

// Enrich adds profile metadata from the proxy without making login depend on it.
func (c *Client) Enrich(ctx context.Context, baseURL, tokenHeader string, credential Credential) Credential {
	if credential.Key == "" || strings.TrimSpace(baseURL) == "" {
		return credential
	}
	if tokenHeader == "" {
		tokenHeader = DefaultTokenHeader
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/user", nil)
	if err != nil {
		return credential
	}
	request.Header.Set("Authorization", "Bearer "+credential.Key)
	request.Header.Set("X-XAI-Token-Auth", tokenHeader)
	request.Header.Set("x-grok-client-version", version.Current)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return credential
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return credential
	}
	var profile userInfo
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&profile) != nil || profile.UserID == "" {
		return credential
	}
	credential.mergeProfile(profile)
	return credential
}

func (c *Credential) mergeProfile(profile userInfo) {
	c.UserID = profile.UserID
	mergeString := func(target *string, value *string) {
		if value != nil {
			*target = *value
		}
	}
	if profile.Email != nil && *profile.Email != "" {
		c.Email = *profile.Email
	}
	mergeString(&c.FirstName, profile.FirstName)
	mergeString(&c.LastName, profile.LastName)
	mergeString(&c.ProfileImageAssetID, profile.ProfileImageAssetID)
	mergeString(&c.PrincipalType, profile.PrincipalType)
	mergeString(&c.PrincipalID, profile.PrincipalID)
	mergeString(&c.TeamID, profile.TeamID)
	mergeString(&c.TeamName, profile.TeamName)
	mergeString(&c.TeamRole, profile.TeamRole)
	mergeString(&c.OrganizationID, profile.OrganizationID)
	mergeString(&c.OrganizationName, profile.OrganizationName)
	mergeString(&c.OrganizationRole, profile.OrganizationRole)
	mergeString(&c.UserBlockedReason, profile.UserBlockedReason)
	if profile.TeamBlockedReasons != nil {
		c.TeamBlockedReasons = append([]string(nil), (*profile.TeamBlockedReasons)...)
	}
	// This implementation follows the privacy build and never enables retention.
	c.CodingDataRetentionOptOut = true
}

func (c *Credential) carryProfileFrom(previous Credential) {
	c.UserID = previous.UserID
	c.Email = previous.Email
	c.FirstName = previous.FirstName
	c.LastName = previous.LastName
	c.ProfileImageAssetID = previous.ProfileImageAssetID
	c.PrincipalType = previous.PrincipalType
	c.PrincipalID = previous.PrincipalID
	c.TeamID = previous.TeamID
	c.TeamName = previous.TeamName
	c.TeamRole = previous.TeamRole
	c.OrganizationID = previous.OrganizationID
	c.OrganizationName = previous.OrganizationName
	c.OrganizationRole = previous.OrganizationRole
	c.UserBlockedReason = previous.UserBlockedReason
	c.TeamBlockedReasons = append([]string(nil), previous.TeamBlockedReasons...)
	c.CodingDataRetentionOptOut = previous.CodingDataRetentionOptOut
}
