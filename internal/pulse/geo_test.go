package pulse

import "testing"

func TestZoneCountryMapsKnownZonesOnly(t *testing.T) {
	cc, lat, lon, ok := ZoneCountry("Europe/Berlin")
	if !ok || cc != "DE" || lat < 45 || lat > 56 || lon < 4 || lon > 16 {
		t.Fatalf("Europe/Berlin = %s (%v, %v) %v; want DE near Germany", cc, lat, lon, ok)
	}
	for _, z := range []string{"Etc/UTC", "other", "Mars/Olympus", ""} {
		if _, _, _, ok := ZoneCountry(z); ok {
			t.Errorf("%q must not map to a country", z)
		}
	}
}
