package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// channelRuleCases is testdata/channel_rules.json: the channel rules case by
// case, the same cases the web's mirror (web/src/lib/channelRules.ts) is
// held to, so a control is offered only where the server allows the act.
type channelRuleCases struct {
	ManageChannel []struct {
		Role          store.Role `json:"role"`
		User          string     `json:"user"`
		ChannelOwner  string     `json:"channelOwner"`
		SectionOwners []string   `json:"sectionOwners"`
		Want          bool       `json:"want"`
	} `json:"manageChannel"`
	PlaceChannel []struct {
		Role    store.Role `json:"role"`
		User    string     `json:"user"`
		Policy  string     `json:"policy"`
		Section []string   `json:"section"`
		Want    bool       `json:"want"`
	} `json:"placeChannel"`
	CreateSection []struct {
		Role   store.Role `json:"role"`
		Policy string     `json:"policy"`
		Want   bool       `json:"want"`
	} `json:"createSection"`
	ManageSection []struct {
		Role   store.Role `json:"role"`
		User   string     `json:"user"`
		Owners []string   `json:"owners"`
		Want   bool       `json:"want"`
	} `json:"manageSection"`
	DeleteChannel []struct {
		Role store.Role `json:"role"`
		Want bool       `json:"want"`
	} `json:"deleteChannel"`
}

func TestChannelRulesFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "channel_rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c channelRuleCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for _, k := range c.ManageChannel {
		var sec *store.RoomSection
		if len(k.SectionOwners) > 0 {
			sec = &store.RoomSection{Owners: k.SectionOwners}
		}
		if got := mayManageChannel(k.Role, k.User, store.RoomChannel{OwnerID: k.ChannelOwner}, sec); got != k.Want {
			t.Errorf("mayManageChannel(%+v) = %v", k, got)
		}
	}
	for _, k := range c.PlaceChannel {
		var sec *store.RoomSection
		if k.Section != nil {
			sec = &store.RoomSection{Owners: k.Section}
		}
		if got := mayPlaceChannel(store.Room{ChannelCreation: k.Policy}, k.Role, k.User, sec); got != k.Want {
			t.Errorf("mayPlaceChannel(%+v) = %v", k, got)
		}
	}
	for _, k := range c.CreateSection {
		if got := mayCreateSection(store.Room{ChannelCreation: k.Policy}, k.Role); got != k.Want {
			t.Errorf("mayCreateSection(%+v) = %v", k, got)
		}
	}
	for _, k := range c.ManageSection {
		if got := mayManageSection(k.Role, k.User, store.RoomSection{Owners: k.Owners}); got != k.Want {
			t.Errorf("mayManageSection(%+v) = %v", k, got)
		}
	}
	// Deleting a channel is the room's owner's and admins' (handleDeleteChannel).
	for _, k := range c.DeleteChannel {
		if got := k.Role.CanInvite(); got != k.Want {
			t.Errorf("delete as %s = %v", k.Role, got)
		}
	}
}
