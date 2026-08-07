import { basename } from 'node:path';

function safeFileName(path) {
	const name = basename(path || 'pulse-local-merge.json').replace(/\.json$/i, '');
	return `${name}.reviewed.json`;
}

export function renderLocalMergeReview(preview, previewPath) {
	if (preview?.schema !== 'pulse.local_store_merge_preview.v1') {
		throw new Error('unsupported local memory preview');
	}
	const payload = Buffer.from(JSON.stringify(preview), 'utf8').toString('base64');
	const conflicts = Array.isArray(preview.conflicts) ? preview.conflicts : [];
	const targetName = safeFileName(previewPath);
	return `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pulse — перенос личной памяти</title>
  <style>
    :root { color-scheme:light; --ink:#302b2e; --muted:#746d72; --line:#e9e1e2; --accent:#b85464; --paper:#fffdfb; }
    * { box-sizing:border-box; }
    body { margin:0; font:16px/1.5 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; color:var(--ink); background:#f7f1ef; }
    main { width:min(880px,calc(100% - 32px)); margin:40px auto 72px; }
    header,article,.done { background:var(--paper); border:1px solid var(--line); border-radius:18px; padding:24px; box-shadow:0 14px 40px rgba(74,52,59,.08); }
    h1 { margin:0 0 8px; font-size:clamp(30px,6vw,48px); line-height:1.05; }
    h2 { margin:0 0 14px; font-size:20px; }
    p { margin:8px 0; }
    .muted { color:var(--muted); }
    .summary { display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr)); gap:10px; margin-top:20px; }
    .summary div { border:1px solid var(--line); border-radius:13px; padding:13px; }
    .summary b { display:block; font-size:25px; }
    article { margin-top:14px; }
    fieldset { border:0; padding:0; margin:0; display:grid; gap:9px; }
    label { display:block; border:1px solid var(--line); border-radius:12px; padding:12px; cursor:pointer; }
    label:has(input:checked) { border-color:var(--accent); background:#fff4f5; }
    input { margin-right:9px; }
    button { margin-top:18px; border:0; border-radius:999px; padding:12px 18px; background:var(--accent); color:white; font:inherit; font-weight:650; cursor:pointer; }
    button:focus-visible,input:focus-visible { outline:3px solid #e7aab4; outline-offset:3px; }
    #status { min-height:24px; margin-top:12px; color:var(--accent); font-weight:600; }
    code { display:block; margin-top:10px; padding:12px; border-radius:10px; background:#f5efed; overflow-wrap:anywhere; }
  </style>
</head>
<body>
<main>
  <header>
    <h1>Собираем личную память</h1>
    <p>Pulse уже подготовил новую базу рядом со старой. Исходные файлы не изменены.</p>
    <div class="summary">
      <div><b>${Number(preview.totals?.events_created ?? 0)}</b><span>новых событий</span></div>
      <div><b>${Number(preview.totals?.capsules_created ?? 0)}</b><span>новых воспоминаний</span></div>
      <div><b>${Number(preview.totals?.events_deduplicated ?? 0)}</b><span>повторов объединено</span></div>
      <div><b>${conflicts.length}</b><span>противоречий</span></div>
    </div>
  </header>
  <section id="conflicts"></section>
  <section class="done">
    <h2>${conflicts.length ? 'Когда выберешь' : 'Противоречий нет'}</h2>
    <p class="muted">Скачай файл с решениями. После этого Pulse сможет одним переключением заменить рабочую базу, сохранив старую как резервную копию.</p>
    <button id="save" type="button">${conflicts.length ? 'Скачать выбранные решения' : 'Скачать готовый файл'}</button>
    <div id="status" role="status" aria-live="polite"></div>
    <p class="muted">Затем выполни:</p>
    <code>pulse migrate commit ~/Downloads/${targetName} --confirm "merge local pulse memory"</code>
  </section>
</main>
<script>
const preview=JSON.parse(new TextDecoder().decode(Uint8Array.from(atob('${payload}'),c=>c.charCodeAt(0))));
const root=document.querySelector('#conflicts');
const clean=value=>String(value??'').trim()||'Без текста';
for(const conflict of preview.conflicts??[]){
  const article=document.createElement('article');
  const title=document.createElement('h2');
  title.textContent=clean(conflict.question);
  article.append(title);
  const group=document.createElement('fieldset');
  for(const choice of conflict.choices??[]){
    const label=document.createElement('label');
    const input=document.createElement('input');
    input.type='radio'; input.name=conflict.id; input.value=choice.id;
    if(conflict.selected===choice.id) input.checked=true;
    label.append(input,document.createTextNode(clean(choice.label)));
    group.append(label);
  }
  article.append(group); root.append(article);
}
document.querySelector('#save').addEventListener('click',()=>{
  for(const conflict of preview.conflicts??[]){
    const selected=document.querySelector('input[name="'+CSS.escape(conflict.id)+'"]:checked');
    if(!selected){ document.querySelector('#status').textContent='Сначала выбери вариант в каждом противоречии.'; return; }
    conflict.selected=selected.value;
  }
  preview.status='reviewed';
  const blob=new Blob([JSON.stringify(preview,null,2)+'\\n'],{type:'application/json'});
  const link=document.createElement('a'); link.href=URL.createObjectURL(blob); link.download='${targetName}'; link.click();
  setTimeout(()=>URL.revokeObjectURL(link.href),1000);
  document.querySelector('#status').textContent='Файл скачан. Теперь выполни команду, показанную ниже.';
});
</script>
</body>
</html>`;
}
