package accountcheckout

// FlaggedLabel is the SQL expression that prefixes a squad's label with the
// flag of the country the server catalogue (`c`, server_catalog) gives it.
// A flag is the two regional-indicator letters of the ISO code, built in SQL
// so both offers render it without another round trip.
func FlaggedLabel(label string) string {
	return `CASE WHEN c.country_code IS NULL THEN ` + label + `
		ELSE chr(127397 + ascii(substr(c.country_code, 1, 1))) || chr(127397 + ascii(substr(c.country_code, 2, 1)))
			|| ' ' || ` + label + ` END`
}
