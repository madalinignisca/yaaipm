// Gantt chart renderer — vanilla JS, no dependencies.
// Renders a horizontally-scrollable timeline with date grid,
// grouped rows (features > tasks > subtasks), and status-colored bars.

function renderGantt(selector, tickets) {
    var container = document.querySelector(selector);
    if (!container || !tickets || tickets.length === 0) return;
    while (container.firstChild) container.removeChild(container.firstChild);

    // -- Parse & prepare data --
    var parsed = tickets.map(function(t) {
        return {
            id: t.ID,
            parentId: t.ParentID || null,
            type: t.Type,
            title: t.Title,
            status: t.Status,
            priority: t.Priority,
            start: new Date(t.DateStart),
            end: new Date(t.DateEnd)
        };
    }).filter(function(t) {
        return !isNaN(t.start) && !isNaN(t.end);
    });

    if (parsed.length === 0) return;

    // Build hierarchy: features (no parent), then children grouped under parents
    var byParent = {};
    var topLevel = [];
    parsed.forEach(function(t) {
        if (!t.parentId) {
            topLevel.push(t);
        } else {
            if (!byParent[t.parentId]) byParent[t.parentId] = [];
            byParent[t.parentId].push(t);
        }
    });

    topLevel.sort(function(a, b) { return a.start - b.start; });

    // Build ordered flat list with depth
    var rows = [];
    function addWithChildren(ticket, depth) {
        ticket.depth = depth;
        rows.push(ticket);
        var children = byParent[ticket.id];
        if (children) {
            children.sort(function(a, b) { return a.start - b.start; });
            children.forEach(function(c) { addWithChildren(c, depth + 1); });
        }
    }
    topLevel.forEach(function(t) { addWithChildren(t, 0); });

    // Orphan children (parent not in gantt view)
    parsed.forEach(function(t) {
        if (rows.indexOf(t) === -1) {
            t.depth = 0;
            rows.push(t);
        }
    });

    // -- Calculate date range --
    var minDate = rows[0].start;
    var maxDate = rows[0].end;
    rows.forEach(function(t) {
        if (t.start < minDate) minDate = t.start;
        if (t.end > maxDate) maxDate = t.end;
    });

    // Padding: 3 days before, 3 days after
    var rangeStart = new Date(minDate);
    rangeStart.setDate(rangeStart.getDate() - 3);
    var rangeEnd = new Date(maxDate);
    rangeEnd.setDate(rangeEnd.getDate() + 3);

    var totalDays = Math.ceil((rangeEnd - rangeStart) / 86400000) + 1;

    // -- Layout constants --
    var LABEL_WIDTH = 220;
    var DAY_WIDTH = 36;
    var ROW_HEIGHT = 36;
    var HEADER_HEIGHT = 48;
    var BAR_HEIGHT = 20;
    var BAR_OFFSET = (ROW_HEIGHT - BAR_HEIGHT) / 2;
    var GRID_WIDTH = totalDays * DAY_WIDTH;

    // -- Status colors --
    var STATUS_COLORS = {
        backlog:       { bg: '#94a3b8', fg: '#fff' },
        ready:         { bg: '#3b82f6', fg: '#fff' },
        planning:      { bg: '#8b5cf6', fg: '#fff' },
        plan_review:   { bg: '#a78bfa', fg: '#fff' },
        implementing:  { bg: '#f59e0b', fg: '#fff' },
        testing:       { bg: '#f97316', fg: '#fff' },
        review:        { bg: '#6366f1', fg: '#fff' },
        done:          { bg: '#22c55e', fg: '#fff' },
        cancelled:     { bg: '#ef4444', fg: '#fff' }
    };

    function getColor(status) {
        return STATUS_COLORS[status] || STATUS_COLORS.backlog;
    }

    // -- Build DOM --
    var wrapper = document.createElement('div');
    wrapper.className = 'gantt-wrapper';

    // Left panel (fixed labels)
    var leftPanel = document.createElement('div');
    leftPanel.className = 'gantt-left';
    leftPanel.style.width = LABEL_WIDTH + 'px';

    var leftHeader = document.createElement('div');
    leftHeader.className = 'gantt-left-header';
    leftHeader.style.height = HEADER_HEIGHT + 'px';
    leftHeader.textContent = 'Ticket';
    leftPanel.appendChild(leftHeader);

    var leftBody = document.createElement('div');
    leftBody.className = 'gantt-left-body';

    rows.forEach(function(t) {
        var row = document.createElement('div');
        row.className = 'gantt-left-row';
        row.style.height = ROW_HEIGHT + 'px';

        var indent = document.createElement('span');
        indent.style.paddingLeft = (t.depth * 16) + 'px';

        var icon = document.createElement('span');
        icon.className = 'gantt-type-icon';
        if (t.type === 'feature') {
            icon.textContent = '\u25C6';
            icon.style.color = '#6366f1';
        } else if (t.type === 'bug') {
            icon.textContent = '\u25CF';
            icon.style.color = '#ef4444';
        } else {
            icon.textContent = '\u25B8';
            icon.style.color = '#94a3b8';
        }

        var label = document.createElement('span');
        label.className = 'gantt-left-label';
        label.textContent = t.title;
        label.title = t.title;
        if (t.depth === 0) label.style.fontWeight = '600';

        row.appendChild(indent);
        row.appendChild(icon);
        row.appendChild(label);
        leftBody.appendChild(row);
    });

    leftPanel.appendChild(leftBody);

    // Right panel (scrollable grid)
    var rightPanel = document.createElement('div');
    rightPanel.className = 'gantt-right';

    var rightInner = document.createElement('div');
    rightInner.className = 'gantt-right-inner';
    rightInner.style.width = GRID_WIDTH + 'px';

    // Header: month labels + day numbers
    var headerEl = document.createElement('div');
    headerEl.className = 'gantt-header';
    headerEl.style.height = HEADER_HEIGHT + 'px';
    headerEl.style.width = GRID_WIDTH + 'px';

    var monthNames = ['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];
    var today = new Date();
    today.setHours(0, 0, 0, 0);

    // Month spans row
    var monthRow = document.createElement('div');
    monthRow.className = 'gantt-month-row';
    var prevMonth = -1;
    var monthStartX = 0;
    var d, date, m;
    for (d = 0; d <= totalDays; d++) {
        date = new Date(rangeStart);
        date.setDate(date.getDate() + d);
        m = date.getMonth();
        if (m !== prevMonth) {
            if (prevMonth !== -1) {
                var mSpan = document.createElement('div');
                mSpan.className = 'gantt-month-span';
                mSpan.style.left = monthStartX + 'px';
                mSpan.style.width = (d * DAY_WIDTH - monthStartX) + 'px';
                var prevDate = new Date(rangeStart);
                prevDate.setDate(prevDate.getDate() + d - 1);
                mSpan.textContent = monthNames[prevMonth] + ' ' + prevDate.getFullYear();
                monthRow.appendChild(mSpan);
            }
            monthStartX = d * DAY_WIDTH;
            prevMonth = m;
        }
    }
    // Last month
    if (prevMonth !== -1) {
        var lastSpan = document.createElement('div');
        lastSpan.className = 'gantt-month-span';
        lastSpan.style.left = monthStartX + 'px';
        lastSpan.style.width = (GRID_WIDTH - monthStartX) + 'px';
        lastSpan.textContent = monthNames[prevMonth] + ' ' + rangeEnd.getFullYear();
        monthRow.appendChild(lastSpan);
    }
    headerEl.appendChild(monthRow);

    // Day numbers row
    var dayRow = document.createElement('div');
    dayRow.className = 'gantt-day-row';
    for (d = 0; d < totalDays; d++) {
        date = new Date(rangeStart);
        date.setDate(date.getDate() + d);
        var dayEl = document.createElement('div');
        dayEl.className = 'gantt-day-cell';
        dayEl.style.left = (d * DAY_WIDTH) + 'px';
        dayEl.style.width = DAY_WIDTH + 'px';
        dayEl.textContent = date.getDate();
        if (date.getDay() === 0 || date.getDay() === 6) {
            dayEl.classList.add('gantt-weekend');
        }
        if (date.getTime() === today.getTime()) {
            dayEl.classList.add('gantt-today-cell');
        }
        dayRow.appendChild(dayEl);
    }
    headerEl.appendChild(dayRow);
    rightInner.appendChild(headerEl);

    // Grid body with bars
    var bodyEl = document.createElement('div');
    bodyEl.className = 'gantt-body';
    bodyEl.style.width = GRID_WIDTH + 'px';

    // Weekend column stripes
    for (d = 0; d < totalDays; d++) {
        date = new Date(rangeStart);
        date.setDate(date.getDate() + d);
        if (date.getDay() === 0 || date.getDay() === 6) {
            var stripe = document.createElement('div');
            stripe.className = 'gantt-weekend-col';
            stripe.style.left = (d * DAY_WIDTH) + 'px';
            stripe.style.width = DAY_WIDTH + 'px';
            stripe.style.height = (rows.length * ROW_HEIGHT) + 'px';
            bodyEl.appendChild(stripe);
        }
    }

    // Today line
    var todayOffset = Math.floor((today - rangeStart) / 86400000);
    if (todayOffset >= 0 && todayOffset < totalDays) {
        var todayLine = document.createElement('div');
        todayLine.className = 'gantt-today-line';
        todayLine.style.left = (todayOffset * DAY_WIDTH + DAY_WIDTH / 2) + 'px';
        todayLine.style.height = (rows.length * ROW_HEIGHT) + 'px';
        bodyEl.appendChild(todayLine);
    }

    // Row backgrounds + bars
    rows.forEach(function(t, i) {
        var rowBg = document.createElement('div');
        rowBg.className = 'gantt-row-bg' + (i % 2 === 0 ? '' : ' gantt-row-alt');
        rowBg.style.top = (i * ROW_HEIGHT) + 'px';
        rowBg.style.height = ROW_HEIGHT + 'px';
        rowBg.style.width = GRID_WIDTH + 'px';
        bodyEl.appendChild(rowBg);

        // Bar
        var startOff = (t.start - rangeStart) / 86400000;
        var duration = (t.end - t.start) / 86400000 + 1;
        var barLeft = startOff * DAY_WIDTH;
        var barWidth = Math.max(duration * DAY_WIDTH, DAY_WIDTH * 0.5);

        var bar = document.createElement('div');
        bar.className = 'gantt-bar';
        bar.style.left = barLeft + 'px';
        bar.style.top = (i * ROW_HEIGHT + BAR_OFFSET) + 'px';
        bar.style.width = barWidth + 'px';
        bar.style.height = BAR_HEIGHT + 'px';

        var color = getColor(t.status);
        bar.style.background = color.bg;
        bar.style.color = color.fg;

        if (t.type === 'feature') {
            bar.classList.add('gantt-bar-feature');
        }

        // Bar label (show title if bar is wide enough)
        if (barWidth > 60) {
            var barLabel = document.createElement('span');
            barLabel.className = 'gantt-bar-label';
            barLabel.textContent = t.title;
            bar.appendChild(barLabel);
        }

        // Tooltip
        var startStr = t.start.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
        var endStr = t.end.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
        bar.title = t.title + '\n' + startStr + ' \u2192 ' + endStr + '\nStatus: ' + t.status;

        // Click to navigate
        bar.style.cursor = 'pointer';
        bar.setAttribute('data-id', t.id);
        bar.addEventListener('click', function() {
            window.location.href = '/tickets/' + this.getAttribute('data-id');
        });

        bodyEl.appendChild(bar);
    });

    bodyEl.style.height = (rows.length * ROW_HEIGHT) + 'px';
    rightInner.appendChild(bodyEl);
    rightPanel.appendChild(rightInner);

    wrapper.appendChild(leftPanel);
    wrapper.appendChild(rightPanel);
    container.appendChild(wrapper);

    // Sync vertical scroll between left labels and right grid
    rightPanel.addEventListener('scroll', function() {
        leftBody.style.transform = 'translateY(-' + rightPanel.scrollTop + 'px)';
    });

    // Scroll to today or first bar
    if (todayOffset >= 0 && todayOffset < totalDays) {
        rightPanel.scrollLeft = Math.max(0, todayOffset * DAY_WIDTH - rightPanel.clientWidth / 3);
    } else {
        var firstOff = (rows[0].start - rangeStart) / 86400000;
        rightPanel.scrollLeft = Math.max(0, firstOff * DAY_WIDTH - 40);
    }
}
