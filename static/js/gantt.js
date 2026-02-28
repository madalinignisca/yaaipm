// Minimal Gantt chart renderer (pure JS, no D3 dependency for now)
function renderGantt(selector, tickets) {
    var container = document.querySelector(selector);
    if (!container || !tickets || tickets.length === 0) return;

    while (container.firstChild) {
        container.removeChild(container.firstChild);
    }

    var wrapper = document.createElement('div');
    wrapper.className = 'stack-sm';

    tickets.forEach(function(t) {
        var row = document.createElement('div');
        row.className = 'gantt-row';

        var label = document.createElement('div');
        label.className = 'gantt-label truncate';
        label.textContent = t.Title || t.title;
        row.appendChild(label);

        var bar = document.createElement('div');
        bar.className = 'gantt-bar';
        bar.title = (t.DateStart || t.date_start) + ' \u2192 ' + (t.DateEnd || t.date_end);
        row.appendChild(bar);

        var dates = document.createElement('div');
        dates.className = 'gantt-dates';
        dates.textContent = (t.DateStart || t.date_start || '') + ' \u2192 ' + (t.DateEnd || t.date_end || '');
        row.appendChild(dates);

        wrapper.appendChild(row);
    });

    container.appendChild(wrapper);
}
