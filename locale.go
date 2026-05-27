package client

import (
	"fmt"
	"os"
	"strings"
	"time"
)

var tzCountryMap = map[string]string{
	"America/New_York":               "us",
	"America/Chicago":                "us",
	"America/Denver":                 "us",
	"America/Los_Angeles":            "us",
	"America/Phoenix":                "us",
	"America/Anchorage":              "us",
	"Pacific/Honolulu":               "us",
	"America/Detroit":                "us",
	"America/Indianapolis":           "us",
	"America/Boise":                  "us",
	"America/Juneau":                 "us",
	"America/Adak":                   "us",
	"America/Nome":                   "us",
	"America/Sitka":                  "us",
	"America/Yakutat":                "us",
	"America/Menominee":              "us",
	"America/Kentucky/Louisville":    "us",
	"America/Kentucky/Monticello":    "us",
	"America/Indiana/Indianapolis":   "us",
	"America/North_Dakota/Beulah":    "us",
	"America/Toronto":                "ca",
	"America/Vancouver":              "ca",
	"America/Edmonton":               "ca",
	"America/Winnipeg":               "ca",
	"America/Halifax":                "ca",
	"America/St_Johns":               "ca",
	"America/Regina":                 "ca",
	"America/Montreal":               "ca",
	"America/Mexico_City":            "mx",
	"America/Cancun":                 "mx",
	"America/Tijuana":                "mx",
	"America/Sao_Paulo":              "br",
	"America/Fortaleza":              "br",
	"America/Manaus":                 "br",
	"America/Bogota":                 "co",
	"America/Lima":                   "pe",
	"America/Santiago":               "cl",
	"America/Buenos_Aires":           "ar",
	"America/Argentina/Buenos_Aires": "ar",
	"Europe/London":                  "gb",
	"Europe/Paris":                   "fr",
	"Europe/Berlin":                  "de",
	"Europe/Rome":                    "it",
	"Europe/Madrid":                  "es",
	"Europe/Amsterdam":               "nl",
	"Europe/Brussels":                "be",
	"Europe/Zurich":                  "ch",
	"Europe/Vienna":                  "at",
	"Europe/Stockholm":               "se",
	"Europe/Oslo":                    "no",
	"Europe/Copenhagen":              "dk",
	"Europe/Helsinki":                "fi",
	"Europe/Warsaw":                  "pl",
	"Europe/Prague":                  "cz",
	"Europe/Budapest":                "hu",
	"Europe/Bucharest":               "ro",
	"Europe/Athens":                  "gr",
	"Europe/Istanbul":                "tr",
	"Europe/Moscow":                  "ru",
	"Europe/Kiev":                    "ua",
	"Europe/Lisbon":                  "pt",
	"Europe/Dublin":                  "ie",
	"Asia/Tokyo":                     "jp",
	"Asia/Seoul":                     "kr",
	"Asia/Shanghai":                  "cn",
	"Asia/Hong_Kong":                 "hk",
	"Asia/Taipei":                    "tw",
	"Asia/Singapore":                 "sg",
	"Asia/Kolkata":                   "in",
	"Asia/Calcutta":                  "in",
	"Asia/Bangkok":                   "th",
	"Asia/Jakarta":                   "id",
	"Asia/Manila":                    "ph",
	"Asia/Kuala_Lumpur":              "my",
	"Asia/Dubai":                     "ae",
	"Asia/Riyadh":                    "sa",
	"Asia/Jerusalem":                 "il",
	"Asia/Tehran":                    "ir",
	"Australia/Sydney":               "au",
	"Australia/Melbourne":            "au",
	"Australia/Brisbane":             "au",
	"Australia/Perth":                "au",
	"Pacific/Auckland":               "nz",
	"Africa/Cairo":                   "eg",
	"Africa/Lagos":                   "ng",
	"Africa/Johannesburg":            "za",
}

var tzViewboxMap = map[string][4]float64{
	"America/Los_Angeles": {-124.0, 38.0, -117.0, 32.5},
	"America/Denver":      {-109.5, 41.0, -102.0, 36.5},
	"America/Chicago":     {-97.5, 42.5, -87.5, 36.5},
	"America/New_York":    {-80.0, 42.0, -72.0, 38.5},
	"America/Phoenix":     {-115.0, 37.0, -109.0, 31.0},
	"America/Anchorage":   {-170.0, 72.0, -130.0, 54.0},
	"Pacific/Honolulu":    {-160.5, 22.5, -154.5, 18.5},
	"America/Detroit":     {-84.5, 43.0, -82.0, 41.5},
	"America/Boise":       {-117.5, 49.0, -111.0, 42.0},
	"America/Toronto":     {-80.0, 44.5, -78.5, 43.0},
	"America/Vancouver":   {-124.0, 49.5, -122.5, 48.5},
	"Europe/London":       {-1.0, 52.0, 0.5, 51.0},
	"Europe/Paris":        {1.5, 49.5, 3.0, 48.0},
	"Europe/Berlin":       {12.5, 53.0, 14.0, 52.0},
	"Europe/Amsterdam":    {4.0, 53.0, 5.5, 51.5},
	"Europe/Madrid":       {-4.5, 41.0, -3.0, 39.5},
	"Europe/Rome":         {11.5, 42.5, 13.0, 41.5},
	"Europe/Zurich":       {7.5, 48.0, 9.0, 46.5},
	"Europe/Stockholm":    {17.5, 60.0, 19.0, 59.0},
	"Asia/Tokyo":          {139.0, 36.0, 140.5, 35.0},
	"Asia/Shanghai":       {120.5, 31.5, 122.0, 30.5},
	"Asia/Singapore":      {103.5, 1.5, 104.0, 1.0},
	"Asia/Kolkata":        {72.0, 19.5, 73.5, 18.5},
	"Asia/Dubai":          {55.0, 25.5, 55.5, 25.0},
	"Asia/Seoul":          {126.5, 37.7, 127.5, 37.3},
	"Asia/Bangkok":        {100.0, 14.0, 101.0, 13.5},
	"Australia/Sydney":    {150.5, -33.5, 151.5, -34.0},
	"Australia/Melbourne": {144.5, -37.5, 145.5, -38.0},
	"Pacific/Auckland":    {174.5, -36.5, 175.5, -37.0},
	"Africa/Johannesburg": {27.5, -26.0, 28.5, -26.5},
	"Africa/Lagos":        {3.0, 6.7, 4.0, 6.3},
	"America/Sao_Paulo":   {-47.0, -23.0, -46.0, -24.0},
	"America/Mexico_City": {-99.5, 19.7, -98.5, 19.0},
}

var detectedCountryCode string

func init() {
	detectedCountryCode = detectCountryCode()
}

func resolveTimezone() string {
	tz := time.Now().Location().String()
	if tz != "Local" {
		return tz
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if idx := strings.Index(link, "zoneinfo/"); idx >= 0 {
			return link[idx+len("zoneinfo/"):]
		}
	}
	return tz
}

func detectCountryCode() string {
	tz := resolveTimezone()
	if cc, ok := tzCountryMap[tz]; ok {
		return cc
	}
	for prefix, cc := range map[string]string{
		"America/": "us",
		"US/":      "us",
		"Canada/":  "ca",
		"Europe/":  "",
		"Asia/":    "",
		"Africa/":  "",
		"Pacific/": "",
	} {
		if strings.HasPrefix(tz, prefix) && cc != "" {
			return cc
		}
	}
	return ""
}

func CountryCode() string {
	return detectedCountryCode
}

func LocationBias() string {
	tz := resolveTimezone()
	if vb, ok := tzViewboxMap[tz]; ok {
		return fmt.Sprintf("&viewbox=%.1f,%.1f,%.1f,%.1f&bounded=0", vb[0], vb[1], vb[2], vb[3])
	}
	if cc := detectedCountryCode; cc != "" {
		return "&countrycodes=" + cc
	}
	return ""
}
