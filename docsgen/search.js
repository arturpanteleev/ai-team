(() => {
  'use strict';

  // Меню разделов на телефоне свёрнуто, на широком экране открыто.
  const menu = document.querySelector('.site-menu');
  const narrow = window.matchMedia('(max-width: 860px)');
  const adaptMenu = () => { if (menu) menu.open = !narrow.matches; };
  adaptMenu();
  narrow.addEventListener('change', adaptMenu);

  // Ссылка на якорь внутри свёрнутого блока «Подробнее» раскрывает его.
  const openFragment = () => {
    let id;
    try { id = decodeURIComponent(location.hash.slice(1)); } catch { return; }
    const target = id && document.getElementById(id);
    if (!target) return;
    for (let p = target.parentElement; p; p = p.parentElement) {
      if (p.tagName === 'DETAILS') p.open = true;
    }
    target.scrollIntoView();
  };
  window.addEventListener('hashchange', openFragment);
  openFragment();

  // Широкие таблицы прокручиваются внутри своей рамки, а не всей страницей.
  for (const table of document.querySelectorAll('.markdown table')) {
    const wrap = document.createElement('div');
    wrap.className = 'table-wrap';
    table.replaceWith(wrap);
    wrap.append(table);
  }

  // Кнопка «Скопировать» у блоков кода.
  for (const pre of document.querySelectorAll('.markdown pre')) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'copy-code';
    button.textContent = 'Скопировать';
    button.addEventListener('click', async () => {
      const code = pre.querySelector('code') || pre;
      try {
        await navigator.clipboard.writeText(code.innerText.replace(/\n$/, ''));
        button.textContent = 'Скопировано';
      } catch {
        const range = document.createRange();
        range.selectNodeContents(code);
        const sel = window.getSelection();
        sel.removeAllRanges();
        sel.addRange(range);
        button.textContent = 'Выделено';
      }
      setTimeout(() => { button.textContent = 'Скопировать'; }, 1600);
    });
    pre.append(button);
  }

  // Подсветка текущего раздела в оглавлении страницы.
  const tocLinks = [...document.querySelectorAll('.toc a')];
  if (tocLinks.length && 'IntersectionObserver' in window) {
    const byId = new Map(tocLinks.map(a => [decodeURIComponent(a.hash.slice(1)), a]));
    const headings = [...byId.keys()].map(id => document.getElementById(id)).filter(Boolean);
    const visible = new Set();
    const mark = () => {
      const first = headings.find(h => visible.has(h)) || null;
      let current = first;
      if (!current) {
        current = headings.filter(h => h.getBoundingClientRect().top < 120).pop() || null;
      }
      for (const a of tocLinks) a.classList.toggle('current', current !== null && byId.get(current.id) === a);
    };
    const observer = new IntersectionObserver(entries => {
      for (const e of entries) e.isIntersecting ? visible.add(e.target) : visible.delete(e.target);
      mark();
    }, { rootMargin: '-64px 0px -60% 0px' });
    headings.forEach(h => observer.observe(h));
  }

  // Поиск по разделам всех страниц.
  const search = document.querySelector('.search');
  const input = document.querySelector('#docs-search');
  const panel = document.querySelector('.search-panel');
  const results = document.querySelector('#search-results');
  const status = document.querySelector('#search-status');
  const index = window.aiTeamSearch;
  if (!search || !input || !Array.isArray(index)) return;
  search.hidden = false;

  const normalize = s => s.toLocaleLowerCase('ru').replaceAll('ё', 'е');
  const entries = index.map(e => ({ ...e, hay: normalize(e.title + ' ' + e.text), head: normalize(e.title) }));
  const render = () => {
    const query = normalize(input.value.trim()).slice(0, 200);
    results.replaceChildren();
    panel.hidden = !query;
    if (!query) { status.textContent = ''; return; }
    const words = query.split(/\s+/);
    const score = e => words.filter(w => e.head.includes(w)).length * 10 - (e.url.includes('#') ? 0 : 1);
    const matches = entries.filter(e => words.every(w => e.hay.includes(w))).sort((a, b) => score(b) - score(a));
    status.textContent = matches.length
      ? `Найдено: ${matches.length}${matches.length > 12 ? ', показаны первые 12' : ''}`
      : 'Ничего не нашлось. Попробуйте одно слово или название команды.';
    for (const e of matches.slice(0, 12)) {
      const li = document.createElement('li');
      const a = document.createElement('a');
      a.href = e.url;
      const page = document.createElement('small');
      page.textContent = e.page;
      const title = document.createElement('strong');
      title.textContent = e.title;
      const snippet = document.createElement('p');
      const at = Math.max(0, normalize(e.text).indexOf(words[0]) - 50);
      snippet.textContent = (at ? '…' : '') + e.text.slice(at, at + 170) + (e.text.length > at + 170 ? '…' : '');
      a.append(page, title, snippet);
      li.append(a);
      results.append(li);
    }
  };
  input.addEventListener('input', render);
  input.addEventListener('focus', render);
  document.addEventListener('click', ev => { if (!search.contains(ev.target)) panel.hidden = true; });
  document.addEventListener('keydown', ev => {
    const editing = ev.target instanceof HTMLElement && (ev.target.matches('input,textarea,select') || ev.target.isContentEditable);
    if (ev.key === '/' && !editing && !ev.ctrlKey && !ev.metaKey && !ev.altKey) { ev.preventDefault(); input.focus(); }
    if (ev.key === 'Escape' && document.activeElement === input) { input.value = ''; render(); input.blur(); }
  });
})();
