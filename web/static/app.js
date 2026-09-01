(() => {
  const button = document.querySelector('[data-nav-toggle]');
  const nav = document.querySelector('#primary-nav');
  if (!button || !nav) return;

  const mobile = window.matchMedia('(max-width: 820px)');
  const firstLink = nav.querySelector('nav a');

  const sync = () => {
    const closed = mobile.matches && !document.body.classList.contains('nav-open');
    nav.inert = closed;
    if (closed) nav.setAttribute('aria-hidden', 'true');
    else nav.removeAttribute('aria-hidden');
  };

  const close = (restoreFocus = false) => {
    document.body.classList.remove('nav-open');
    button.setAttribute('aria-expanded', 'false');
    button.querySelector('.sr-only').textContent = '메뉴 열기';
    sync();
    if (restoreFocus && mobile.matches) button.focus();
  };

  const open = () => {
    document.body.classList.add('nav-open');
    button.setAttribute('aria-expanded', 'true');
    button.querySelector('.sr-only').textContent = '메뉴 닫기';
    sync();
    firstLink?.focus();
  };

  button.addEventListener('click', () => {
    if (document.body.classList.contains('nav-open')) close(true);
    else open();
  });

  nav.addEventListener('click', (event) => {
    if (event.target.closest('a')) close(false);
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && document.body.classList.contains('nav-open')) close(true);
  });

  mobile.addEventListener('change', () => close(false));
  sync();
})();

(() => {
  const form = document.querySelector('[data-invite-token-form]');
  if (!form) return;
  const token = new URLSearchParams(window.location.hash.slice(1)).get('token');
  history.replaceState(null, '', window.location.pathname);
  if (!token) {
    const status = form.querySelector('[data-invite-token-status]');
    if (status) status.textContent = '초대 링크가 올바르지 않습니다. 관리자에게 재초대를 요청해 주세요.';
    return;
  }
  form.elements.token.value = token;
  form.requestSubmit();
})();
