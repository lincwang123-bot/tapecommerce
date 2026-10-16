// One binding per page. No global state or persistence.
export function bindNavigation(header) {
  const toggle = header?.querySelector('.nav-toggle'), nav = header?.querySelector('nav');
  if (!toggle || !nav) return () => {};
  const close = () => { nav.dataset.open = 'false'; toggle.setAttribute('aria-expanded','false'); toggle.textContent='菜单'; };
  const click = () => { const open = nav.dataset.open !== 'true'; nav.dataset.open=String(open); toggle.setAttribute('aria-expanded',String(open)); toggle.textContent=open?'关闭':'菜单'; };
  const selected = e => { if(e.target.closest('a')) close(); };
  const outside = e => { if(!header.contains(e.target)) close(); };
  const escape = e => { if(e.key==='Escape' && nav.dataset.open==='true'){close();toggle.focus();} };
  const breakpoint=matchMedia('(min-width:1001px)');
  toggle.addEventListener('click',click);nav.addEventListener('click',selected);
  document.addEventListener('click',outside);document.addEventListener('keydown',escape);breakpoint.addEventListener('change',close);
  close();
  return () => {toggle.removeEventListener('click',click);nav.removeEventListener('click',selected);document.removeEventListener('click',outside);document.removeEventListener('keydown',escape);breakpoint.removeEventListener('change',close);};
}
