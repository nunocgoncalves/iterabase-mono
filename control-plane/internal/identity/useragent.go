package identity

import "strings"

// userAgentMaxBytes bounds how much of a User-Agent header is inspected. The
// raw value is never stored or displayed.
const userAgentMaxBytes = 512

// Bounded normalized client labels. `Other` is used when the value is present
// but unrecognized; empty labels mean the client could not be normalized.
const (
	ClientLabelOther = "Other"

	BrowserChrome  = "Chrome"
	BrowserSafari  = "Safari"
	BrowserFirefox = "Firefox"
	BrowserEdge    = "Edge"

	OSMacOS    = "macOS"
	OSIOS      = "iOS"
	OSWindows  = "Windows"
	OSAndroid  = "Android"
	OSLinux    = "Linux"
	OSChromeOS = "ChromeOS"

	DeviceDesktop = "Desktop"
	DeviceMobile  = "Mobile"
	DeviceTablet  = "Tablet"
)

// ClientLabels is the bounded customer-safe client projection.
type ClientLabels struct {
	Browser string
	OS      string
	Device  string
}

// ParseClientLabels normalizes at most 512 bytes of User-Agent into bounded
// labels. Failure to parse yields an empty or `Other` projection, never a raw
// value.
func ParseClientLabels(userAgent string) ClientLabels {
	if userAgent == "" {
		return ClientLabels{}
	}
	if len(userAgent) > userAgentMaxBytes {
		userAgent = userAgent[:userAgentMaxBytes]
	}
	ua := strings.ToLower(userAgent)
	return ClientLabels{
		Browser: browserLabel(ua),
		OS:      osLabel(ua),
		Device:  deviceLabel(ua),
	}
}

func browserLabel(ua string) string {
	switch {
	case strings.Contains(ua, "edg/"):
		return BrowserEdge
	case strings.Contains(ua, "opr/"), strings.Contains(ua, "opera"):
		return ClientLabelOther
	case strings.Contains(ua, "chrome/"), strings.Contains(ua, "crios/"):
		return BrowserChrome
	case strings.Contains(ua, "firefox/"), strings.Contains(ua, "fxios/"):
		return BrowserFirefox
	case strings.Contains(ua, "safari/"):
		return BrowserSafari
	default:
		return ClientLabelOther
	}
}

func osLabel(ua string) string {
	switch {
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"), strings.Contains(ua, "ipod"):
		return OSIOS
	case strings.Contains(ua, "mac os x"), strings.Contains(ua, "macintosh"):
		return OSMacOS
	case strings.Contains(ua, "android"):
		return OSAndroid
	case strings.Contains(ua, "windows"):
		return OSWindows
	case strings.Contains(ua, "cros"):
		return OSChromeOS
	case strings.Contains(ua, "linux"), strings.Contains(ua, "x11"):
		return OSLinux
	default:
		return ClientLabelOther
	}
}

func deviceLabel(ua string) string {
	switch {
	case strings.Contains(ua, "ipad"), strings.Contains(ua, "tablet"):
		return DeviceTablet
	case strings.Contains(ua, "mobi"), strings.Contains(ua, "iphone"), strings.Contains(ua, "ipod"),
		strings.Contains(ua, "android"):
		return DeviceMobile
	default:
		return DeviceDesktop
	}
}
