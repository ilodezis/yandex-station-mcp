package oauth

import (
	"html/template"
	"math"
	"net/http"
	"strconv"
)

type field struct{ Name, Value string }

type consentView struct {
	Host    string // where the code will be sent, e.g. claude.ai
	TTLDays int
	Hidden  []field
	Fatal   bool   // the request itself is wrong: no form, no retry
	Locked  bool   // too many wrong passwords: the form stays, but it will refuse for a while
	Message string // wrong password, lockout or the fatal reason
}

func renderConsent(w http.ResponseWriter, status int, v consentView) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// No scripts, fonts or images from anywhere: the page is one self-contained document.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = consentTmpl.Execute(w, v)
}

// ledCount is the real ring: 88 RGBW LEDs on the top panel (the framebuffer is 89×1).
const ledCount = 88

type led struct {
	X, Y string
	I    int
}

// ringLEDs places the LEDs clockwise from twelve o'clock on the drawing's 400×400 grid.
var ringLEDs = func() []led {
	const cx, cy, r = 200.0, 200.0, 166.0
	out := make([]led, ledCount)
	for i := range out {
		a := -math.Pi/2 + 2*math.Pi*float64(i)/ledCount
		out[i] = led{
			X: strconv.FormatFloat(cx+r*math.Cos(a), 'f', 2, 64),
			Y: strconv.FormatFloat(cy+r*math.Sin(a), 'f', 2, 64),
			I: i,
		}
	}
	return out
}()

// daysWord is the Russian plural of "день" for n.
func daysWord(n int) string {
	switch n10, n100 := n%10, n%100; {
	case n10 == 1 && n100 != 11:
		return "день"
	case n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return "дня"
	}
	return "дней"
}

// The page reads like the speaker's own product leaflet: light paper, plain type, and the
// speaker drawn from above as a graphite fabric top with its 88 LEDs. The ring is the only
// colour and it answers the way the device would: it fills in warm white (the W of RGBW)
// when the page opens, blinks red on a wrong password, holds amber during a lockout, and on
// a broken request stays dark with a single red pixel.
var consentTmpl = template.Must(template.New("consent").Funcs(template.FuncMap{
	"leds": func() []led { return ringLEDs },
	"days": daysWord,
}).Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="robots" content="noindex">
<meta name="color-scheme" content="light dark">
<title>Станция: подключение приложения</title>
<style>
  :root {
    --paper: #efefeb; --ink: #151515; --ink-2: #5a5a55; --rule: #d2d2cb;
    --rim: #1b1b1d; --fabric: #2a2a2d; --thread: #38383c; --led-off: #424247;
    --warm: #ffe2a6; --red: #ff3b2f; --amber: #ffa516;
    --red-ink: #b42318; --amber-ink: #875000;
    --btn: #151515; --btn-ink: #f4f3ef; --btn-hover: #2c2c2c;
    --shadow: drop-shadow(0 28px 34px rgb(0 0 0 / .16)) drop-shadow(0 6px 8px rgb(0 0 0 / .12));
    color-scheme: light;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --paper: #131314; --ink: #ecebe6; --ink-2: #a3a29b; --rule: #2d2d2f;
      --rim: #0a0a0b; --fabric: #242427; --thread: #303034; --led-off: #3a3a3f;
      --red-ink: #ff8278; --amber-ink: #ffbb52;
      --btn: #ecebe6; --btn-ink: #131314; --btn-hover: #ffffff;
      --shadow: drop-shadow(0 24px 30px rgb(0 0 0 / .5));
      color-scheme: dark;
    }
  }

  * { box-sizing: border-box; }
  html { -webkit-text-size-adjust: 100%; }
  body {
    margin: 0; background: var(--paper); color: var(--ink);
    font: 16px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI Variable Text", "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    -webkit-font-smoothing: antialiased; caret-color: var(--ink);
  }
  ::selection { background: var(--warm); color: #151515; }

  .page {
    display: grid; grid-template-areas: "mark" "device" "content";
    max-width: 560px; margin: 0 auto; overflow-x: clip;
    padding: max(20px, env(safe-area-inset-top)) 22px 44px;
  }
  .mark { grid-area: mark; margin: 0; font-size: 15px; font-weight: 700; letter-spacing: -.01em; }

  .device { grid-area: device; justify-self: end; width: 300px; margin: -6px -96px 0 0; }
  .top { display: block; width: 100%; height: auto; filter: var(--shadow); }
  .rim { fill: var(--rim); }
  .k0 { fill: var(--fabric); }
  .k1 { fill: var(--thread); }
  .led { fill: var(--led-off); }

  .is-ready .led { fill: var(--warm); animation: wake .55s cubic-bezier(.16, 1, .3, 1) backwards; animation-delay: calc(var(--i) * 11ms + .15s); }
  .is-wrong .led { fill: var(--red); animation: reject .9s step-end both; }
  .is-locked .led { fill: var(--amber); }
  .is-fatal .led:first-child { fill: var(--red); }
  @keyframes wake { from { fill: var(--led-off); } }
  @keyframes reject { 18%, 54% { fill: var(--led-off); } 36%, 72% { fill: var(--red); } }
  @keyframes reject-dot { 18%, 54% { opacity: .15; } 36%, 72% { opacity: 1; } }

  .content { grid-area: content; margin-top: 28px; }
  h1 {
    margin: 0 0 24px; font-size: clamp(30px, 8.2vw, 44px); line-height: 1.04; font-weight: 700;
    letter-spacing: -.028em; text-wrap: balance;
  }
  .host { white-space: nowrap; }
  p { margin: 0; }

  .terms { margin: 0 0 34px; padding: 0; border-bottom: 1px solid var(--rule); }
  .terms div { display: grid; grid-template-columns: 4.6em 1fr; gap: 12px; padding: 10px 0; border-top: 1px solid var(--rule); }
  .terms dt { color: var(--ink-2); }
  .terms dd { margin: 0; text-wrap: balance; }

  label { display: block; margin-bottom: 2px; font-size: 13px; font-weight: 600; }
  input[type=password] {
    display: block; width: 100%; padding: 10px 0 9px; border: 0; border-bottom: 1px solid var(--ink-2); border-radius: 0;
    background: transparent; color: var(--ink); font: inherit; font-size: 22px; font-weight: 500; line-height: 1.2; letter-spacing: .14em;
    transition: border-color .15s;
  }
  input[type=password]:focus { outline: none; padding-bottom: 8px; border-bottom: 2px solid var(--ink); }
  .is-wrong input[type=password] { padding-bottom: 8px; border-bottom: 2px solid var(--red-ink); }
  .is-locked input[type=password] { padding-bottom: 8px; border-bottom: 2px solid var(--amber-ink); }
  input[type=password]:-webkit-autofill { -webkit-text-fill-color: var(--ink); box-shadow: inset 0 0 0 40px var(--paper); }

  .msg { display: flex; gap: 10px; align-items: baseline; margin-top: 12px; font-size: 15px; font-weight: 500; color: var(--red-ink); }
  .msg::before { content: ""; flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--red); }
  .is-wrong .msg::before { animation: reject-dot .9s step-end both; }
  .is-locked .msg { color: var(--amber-ink); }
  .is-locked .msg::before { background: var(--amber); }

  button {
    display: block; width: 100%; height: 54px; margin-top: 26px; padding: 0 30px; border: 0; border-radius: 10px;
    background: var(--btn); color: var(--btn-ink); font: inherit; font-size: 17px; font-weight: 600; line-height: 1; cursor: pointer;
    transition: background-color .15s, transform .1s;
  }
  button:hover { background: var(--btn-hover); }
  button:active { transform: translateY(1px); }
  button:focus-visible { outline: 2px solid var(--ink); outline-offset: 3px; }

  .reason { display: flex; gap: 10px; align-items: baseline; padding: 12px 0; border-block: 1px solid var(--rule); font-weight: 500; }
  .reason::before { content: ""; flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--red); }

  @media (min-width: 900px) {
    .page {
      max-width: 1180px; min-height: 100vh; min-height: 100svh; padding: 44px 56px;
      grid-template-columns: minmax(0, 1fr) minmax(0, 430px); grid-template-rows: auto 1fr;
      grid-template-areas: "device mark" "device content"; column-gap: clamp(56px, 8vw, 128px);
    }
    .device { align-self: center; justify-self: center; width: min(100%, 500px); margin: 0; }
    .content { align-self: center; margin: 0; padding-bottom: 24px; }
    button { width: auto; min-width: 220px; }
  }
  @media (prefers-reduced-motion: reduce) {
    *, *::before, *::after { animation: none !important; transition: none !important; }
  }
</style>
</head>
<body class="{{if .Fatal}}is-fatal{{else if .Locked}}is-locked{{else if .Message}}is-wrong{{else}}is-ready{{end}}">
<div class="page">
  <p class="mark">Станция</p>
  <div class="device">
    <svg class="top" viewBox="0 0 400 400" aria-hidden="true" focusable="false">
      <defs>
        <pattern id="knit" width="4" height="4" patternUnits="userSpaceOnUse">
          <rect class="k0" width="4" height="4"/>
          <ellipse class="k1" cx="1" cy="1.3" rx=".95" ry=".6"/>
          <ellipse class="k1" cx="3" cy="3.3" rx=".95" ry=".6"/>
        </pattern>
        <linearGradient id="edge" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stop-color="#fff" stop-opacity=".42"/>
          <stop offset=".45" stop-color="#fff" stop-opacity=".04"/>
          <stop offset="1" stop-color="#fff" stop-opacity=".12"/>
        </linearGradient>
        <radialGradient id="shade" cx=".36" cy=".3" r=".78">
          <stop offset="0" stop-color="#fff" stop-opacity=".07"/>
          <stop offset=".62" stop-color="#000" stop-opacity="0"/>
          <stop offset="1" stop-color="#000" stop-opacity=".38"/>
        </radialGradient>
      </defs>
      <circle class="rim" cx="200" cy="200" r="199"/>
      <circle cx="200" cy="200" r="198.2" fill="none" stroke="url(#edge)" stroke-width="1.6"/>
      <circle cx="200" cy="200" r="191" fill="url(#knit)"/>
      <circle cx="200" cy="200" r="191" fill="url(#shade)"/>
      <g>{{range leds}}<circle class="led" cx="{{.X}}" cy="{{.Y}}" r="3.4" style="--i:{{.I}}"/>{{end}}</g>
    </svg>
  </div>
  <main class="content">
{{if .Fatal}}
    <h1>Запрос отклонён</h1>
    <p class="reason" role="alert">{{.Message}}</p>
{{else}}
    <h1><span class="host">{{.Host}}</span> просит доступ к колонке</h1>
    <dl class="terms">
      <div><dt>Доступ</dt><dd>голос, звуки, таймеры, громкость, кольцо</dd></div>
      <div><dt>Срок</dt><dd>{{if gt .TTLDays 0}}{{.TTLDays}} {{days .TTLDays}}{{else}}меньше суток{{end}}</dd></div>
    </dl>
    <form method="post" action="/oauth/authorize">
      {{range .Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
      {{end}}<input type="text" name="username" value="station" autocomplete="username" hidden>
      <label for="pw">Пароль</label>
      <input id="pw" name="password" type="password" autocomplete="current-password" required autofocus{{if .Message}} aria-invalid="true" aria-describedby="msg"{{end}}>
      {{if .Message}}<p class="msg" id="msg" role="alert">{{.Message}}</p>{{end}}
      <button type="submit">Разрешить</button>
    </form>
{{end}}
  </main>
</div>
</body>
</html>`))
