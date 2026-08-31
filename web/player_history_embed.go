package main

import (
	"bytes"
	_ "embed"
)

//go:embed player_history.js
var playerHistoryJS []byte

func init() {
	autoHTML = bytes.Replace(
		autoHTML,
		[]byte(`<section class="chart-panel"><div class="chart-head"><div><div class="chart-title">players online</div><div class="chart-meta" id="chart-meta">no samples yet</div></div><div class="chart-value" id="chart-value">--</div></div><div class="chart"><div class="axis"><span id="axis-top">--</span><span id="axis-mid">--</span><span>0</span></div><div class="plot" id="player-plot" aria-label="Player count history"></div></div><div class="chart-foot"><span id="chart-start">--</span><span id="chart-end">--</span></div></section>`),
		[]byte(`<section class="chart-panel"></section>`),
		1,
	)
	autoHTML = bytes.Replace(
		autoHTML,
		[]byte(`</body>`),
		append([]byte("<script>"), append(playerHistoryJS, []byte("</script></body>")...)...),
		1,
	)
}
