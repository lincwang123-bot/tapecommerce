// A bounded perspective effect for the original photograph, with no render loop.
const bindings = new WeakMap();
export function bindLaptop(root) {
  const scene = root?.querySelector('[data-laptop-scene]');
  if (!scene) return () => {};
  bindings.get(scene)?.();
  const stage = scene.querySelector('[data-laptop-stage]');
  const object = scene.querySelector('.laptop-object');
  const controls = scene.querySelector('.laptop-controls');
  const resetButton = scene.querySelector('[data-laptop-reset]');
  const motion = matchMedia('(prefers-reduced-motion: reduce)');
  const events = new AbortController();
  const on = (target, type, handler) => target.addEventListener(type, handler, {signal:events.signal});
  let x = 0, y = 0, frame = 0, drag = null, pinned = false;
  const clamp = (value, limit) => Math.max(-limit, Math.min(limit, value));
  const paint = () => {
    frame = 0;
    object.style.setProperty('--laptop-x', x.toFixed(2) + 'deg');
    object.style.setProperty('--laptop-y', y.toFixed(2) + 'deg');
    scene.dataset.posed = String(pinned);
  };
  const pose = (nextX, nextY) => {
    x = clamp(nextX, 16); y = clamp(nextY, 24);
    if (!frame) frame = requestAnimationFrame(paint);
  };
  const release = () => {
    const pointer = drag?.id;
    drag = null;
    scene.dataset.dragging = 'false';
    if (pointer !== undefined && stage.hasPointerCapture(pointer)) stage.releasePointerCapture(pointer);
  };
  const reset = () => { release(); pinned = false; pose(0, 0); };
  const preference = () => {
    reset();
    scene.dataset.interactive = String(!motion.matches);
    controls.hidden = motion.matches;
    stage.tabIndex = motion.matches ? -1 : 0;
    stage.setAttribute('role', motion.matches ? 'img' : 'group');
    stage.setAttribute('aria-label', motion.matches ? '银色笔记本电脑' : '电脑视角预览');
    if (motion.matches) stage.removeAttribute('aria-describedby');
    else stage.setAttribute('aria-describedby', 'laptop-instructions');
  };
  on(stage, 'pointerdown', event => {
    if (motion.matches || !event.isPrimary || event.button !== 0) return;
    if (event.pointerType === 'mouse') event.preventDefault();
    drag = {id:event.pointerId, startX:event.clientX, startY:event.clientY, x, y, moved:false, rect:stage.getBoundingClientRect()};
    scene.dataset.dragging = 'true';
    stage.setPointerCapture(event.pointerId);
  });
  on(stage, 'pointermove', event => {
    if (motion.matches) return;
    if (drag) {
      if (event.pointerId !== drag.id) return;
      const dx = event.clientX - drag.startX, dy = event.clientY - drag.startY;
      if (Math.hypot(dx, dy) < 5 && !drag.moved) return;
      // Vertical touch gestures belong to the page; horizontal swipes turn the image.
      if (event.pointerType === 'touch' && !drag.moved && Math.abs(dy) > Math.abs(dx)) { reset(); return; }
      drag.moved = true; pinned = true;
      pose(event.pointerType === 'touch' ? drag.x : drag.x - dy / drag.rect.height * 45,
           drag.y + dx / drag.rect.width * 70);
    } else if (!pinned && event.pointerType === 'mouse') {
      const rect = stage.getBoundingClientRect();
      pose(clamp((.5 - (event.clientY - rect.top) / rect.height) * 12, 6),
           clamp(((event.clientX - rect.left) / rect.width - .5) * 18, 9));
    }
  });
  on(stage, 'pointerup', event => { if (drag?.id === event.pointerId) release(); });
  on(stage, 'pointercancel', reset);
  on(stage, 'lostpointercapture', () => { if (drag) reset(); });
  on(stage, 'pointerleave', () => { if (!drag && !pinned) pose(0, 0); });
  on(stage, 'keydown', event => {
    if (motion.matches) return;
    if (['Escape', 'Home', 'Enter', ' '].includes(event.key)) { event.preventDefault(); reset(); return; }
    const steps = {ArrowLeft:[0,-4], ArrowRight:[0,4], ArrowUp:[4,0], ArrowDown:[-4,0]};
    const step = steps[event.key];
    if (!step) return;
    event.preventDefault(); pinned = true; pose(x + step[0], y + step[1]);
  });
  on(resetButton, 'click', reset);
  on(motion, 'change', preference);
  on(window, 'blur', () => { if (drag) reset(); });
  on(document, 'visibilitychange', () => { if (document.hidden) reset(); });
  preference();
  const dispose = () => {
    events.abort(); release(); cancelAnimationFrame(frame); frame = 0;
    object.style.removeProperty('--laptop-x'); object.style.removeProperty('--laptop-y');
    scene.dataset.interactive = 'false'; scene.dataset.posed = 'false';
    controls.hidden = true; stage.tabIndex = -1;
    stage.setAttribute('role', 'img'); stage.setAttribute('aria-label', '银色笔记本电脑');
    stage.removeAttribute('aria-describedby'); bindings.delete(scene);
  };
  bindings.set(scene, dispose);
  return dispose;
}
