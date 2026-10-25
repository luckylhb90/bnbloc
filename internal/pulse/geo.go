package pulse

// ZoneCountry maps a client's IANA time zone to the country it is in, and the
// country's placement on the globe. ok is false for a zone the table does not
// know (Etc/*, "other", a zone added after the table was made): such a member is
// counted nowhere. This is the one place that turns a zone into a country.
func ZoneCountry(zone string) (code string, lat, lon float64, ok bool) {
	cc, found := zoneCountry[zone]
	if !found {
		return "", 0, 0, false
	}
	c, found := countryCentres[cc]
	if !found {
		return "", 0, 0, false
	}
	return cc, c.Lat, c.Lon, true
}

// CountryCentreOf returns a country's placement on the globe, by its code.
func CountryCentreOf(code string) (lat, lon float64, ok bool) {
	c, found := countryCentres[code]
	if !found {
		return 0, 0, false
	}
	return c.Lat, c.Lon, true
}
