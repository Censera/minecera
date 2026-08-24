package main

import "bytes"

func init() {
	autoHTML = patchFrontend(autoHTML)
}

func patchFrontend(html []byte) []byte {
	replacements := [][2][]byte{
		{
			[]byte("let stick=true,history=JSON.parse(localStorage.getItem('minecera.command-history')||'[]'),historyIndex=history.length,suggestionIndex=0,currentSuggestions=[];"),
			[]byte("let stick=true,history=[];try{const raw=localStorage.getItem('minecera.command-history');const parsed=raw?JSON.parse(raw):[];history=Array.isArray(parsed)?parsed:[]}catch{history=[]}let historyIndex=history.length,suggestionIndex=0,currentSuggestions=[];"),
		},
		{
			[]byte("return {query,items,slash,prefix:tokens.slice(0,-1).join(' '),command}}"),
			[]byte("return {query,items,slash,prefix:tokens.slice(0,-1).join(' '),command,source:'subcommand'}}"),
		},
		{
			[]byte("execute:['align','anchored','as','at','facing','if','in','positioned','rotated','run','store','summon','unless','unless']"),
			[]byte("execute:['align','anchored','as','at','facing','if','in','positioned','rotated','run','store','summon','unless']"),
		},
		{
			[]byte("fill:['replace','destroy','keep','outline','hollow','destroy','filter']"),
			[]byte("fill:['replace','destroy','keep','outline','hollow','filter']"),
		},
	}

	for _, replacement := range replacements {
		html = bytes.Replace(html, replacement[0], replacement[1], 1)
	}

	const marker = "</script>\n</body>"
	const patch = `<script>
window.renderStatus=function(s){
 const state=String(s.state||'unknown').toLowerCase();
 const dot=state==='running'?'up':state==='offline'?'down':'warn';
 $('state').textContent=s.state||'unknown';
 $('dot').className='dot '+dot;
 $('uptime').textContent=s.uptime||'--';
 $('cpu').textContent=s.cpu||'--';
 $('memory').textContent=s.memory||'--';
 $('load').textContent=s.load||'--';
 $('disk').textContent=s.disk||'--';
 $('backup').textContent=s.lastBackup||((s.backups??'--')+' backups');
 $('updated').textContent=s.updated||'--';
 $('event').textContent=s.journalEvent||'live';
};
</script>
</body>`
	html = bytes.Replace(html, []byte(marker), []byte(patch), 1)
	return html
}
