(() => {
    const logBox = document.getElementById('logs');
    const input = document.getElementById('command');
    const suggestions = document.getElementById('suggestions');
    const eventLabel = document.getElementById('event');
    const stateDot = document.getElementById('dot');
    const stateLabel = document.getElementById('state');
    const actionBar = document.querySelector('.actions');

    let renderedLogs = '';

    const originalCommands = window.COMMANDS || [];
    const originalSubcommands = window.SUBCOMMANDS || {};

    function completionContext(value) {
        const raw = value.trimStart();
        const slash = raw.startsWith('/') ? '/' : '';
        const body = raw.slice(slash.length);
        const trailingSpace = /\s$/.test(body);
        const tokens = body.match(/\S+/g) || [];

        if (tokens.length === 0) {
            return { query: '', items: [], slash, source: 'command', trailingSpace };
        }

        const command = tokens[0].toLowerCase();
        const source = originalSubcommands[command];

        if (source) {
            if (tokens.length === 1 && trailingSpace) {
                return {
                    query: '',
                    items: source.map(value => ({ value, source: 'subcommand' })),
                    slash,
                    command,
                    source: 'subcommand',
                    trailingSpace,
                };
            }

            if (tokens.length >= 2) {
                const query = trailingSpace ? '' : tokens[tokens.length - 1].toLowerCase();
                const items = source
                    .filter(value => value.toLowerCase().startsWith(query))
                    .map(value => ({ value, source: 'subcommand' }));
                return {
                    query,
                    items,
                    slash,
                    command,
                    source: 'subcommand',
                    trailingSpace,
                };
            }
        }

        if (tokens.length > 1 || trailingSpace) {
            return { query: '', items: [], slash, source: 'command', trailingSpace };
        }

        const query = tokens[0].toLowerCase();
        const items = originalCommands
            .filter(value => value.startsWith(query))
            .map(value => ({ value, source: 'command' }));
        return { query, items, slash, source: 'command', trailingSpace };
    }

    function hideSuggestions() {
        suggestions.classList.remove('visible');
        suggestions.innerHTML = '';
        window.currentSuggestions = [];
    }

    window.completionContext = completionContext;
    window.getSuggestions = value => completionContext(value).items.slice(0, 10);

    window.completionInsert = item => {
        const ctx = completionContext(input.value);
        const raw = input.value;
        const leading = raw.match(/^\s*/)?.[0] || '';
        const slash = ctx.slash;
        const body = raw.trimStart().slice(slash.length);
        const parts = body.match(/\S+/g) || [];

        if (ctx.source === 'subcommand') {
            if (ctx.trailingSpace) {
                parts.push(item.value);
            } else {
                parts[parts.length - 1] = item.value;
            }
        } else {
            parts[0] = item.value;
        }

        input.value = leading + slash + parts.join(' ') + ' ';
        input.setSelectionRange(input.value.length, input.value.length);
        hideSuggestions();
    };

    window.renderSuggestions = () => {
        const items = window.getSuggestions(input.value);
        window.currentSuggestions = items;
        window.suggestionIndex = 0;

        if (!items.length) {
            hideSuggestions();
            return;
        }

        const ctx = completionContext(input.value);
        const query = ctx.query;
        const qlen = query.length;

        suggestions.innerHTML = items.map((item, index) => {
            const match = item.value.slice(0, qlen);
            const rest = item.value.slice(qlen);
            return `<div class="suggestion${index === 0 ? ' selected' : ''}" role="option" data-index="${index}"><span class="suggestion-word"><span class="suggestion-prefix">${window.esc(match)}</span><span class="suggestion-rest">${window.esc(rest)}</span></span><span class="suggestion-source">${item.source}</span></div>`;
        }).join('');

        suggestions.classList.add('visible');
        suggestions.querySelectorAll('.suggestion').forEach(element => {
            element.addEventListener('mousedown', e => {
                e.preventDefault();
                window.completionInsert(items[Number(element.dataset.index)]);
                input.focus();
            });
        });
    };

    window.renderLogs = lines => {
        const key = JSON.stringify(lines);
        if (key === renderedLogs) {
            return;
        }
        renderedLogs = key;

        const stick = logBox.scrollHeight - logBox.scrollTop - logBox.clientHeight < 24;
        logBox.innerHTML = lines.length
            ? lines.map(line => {
                let kind = 'info';
                if (/error|exception|failed|fatal/i.test(line)) kind = 'error';
                else if (/warn/i.test(line)) kind = 'warn';
                else if (/healthy|started|stopped|saved|done/i.test(line)) kind = 'ok';
                return `<div class="line ${kind}">${window.esc(line)}</div>`;
            }).join('')
            : '<div class="empty">no log output</div>';

        if (stick) {
            logBox.scrollTop = logBox.scrollHeight;
        }
    };

    window.renderStatus = status => {
        const state = String(status.state || 'unknown').toLowerCase();
        stateLabel.textContent = status.state || 'unknown';
        stateDot.className = 'dot ' + (
            state === 'running' ? 'up' :
            state === 'offline' || state === 'stopped' || state === 'failed' ? 'down' :
            'warn'
        );
        document.getElementById('uptime').textContent = status.uptime || '--';
        document.getElementById('cpu').textContent = status.cpu || '--';
        document.getElementById('memory').textContent = status.memory || '--';
        document.getElementById('load').textContent = status.load || '--';
        document.getElementById('disk').textContent = status.disk || '--';
        document.getElementById('backup').textContent = status.lastBackup || ((status.backups ?? '--') + ' backups');
        document.getElementById('updated').textContent = status.updated || '--';
        eventLabel.textContent = status.journalEvent || 'live';
    };

    window.apply = data => {
        if (data.status) window.renderStatus(data.status);
        if (Array.isArray(data.logs)) window.renderLogs(data.logs);
    };

    if (actionBar) {
        actionBar.style.display = 'flex';
        actionBar.style.flexWrap = 'wrap';
    }

    if (input) {
        input.addEventListener('input', window.renderSuggestions);
    }

    if (window.__mineceraEventSources) {
        for (const source of window.__mineceraEventSources) {
            source.onerror = null;
        }
    }
})();
