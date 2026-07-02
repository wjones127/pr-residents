(function () {
  function fmtTokens(n) {
    n = n || 0;
    if (n >= 1000) return Math.round(n / 1000) + "k";
    return "" + n;
  }

  var refresh = document.getElementById("refresh");
  var dispatch = document.getElementById("dispatch");
  var cancel = document.getElementById("cancel");
  var bar = document.getElementById("bar");
  var status = document.getElementById("status");
  var tokens = document.getElementById("tokens");
  var lanes = document.getElementById("lanes");

  function setBusy(busy) {
    refresh.disabled = busy;
    dispatch.disabled = busy;
    cancel.hidden = !busy;
  }

  function reloadLanes() {
    fetch("/lanes")
      .then(function (r) { return r.text(); })
      .then(function (html) { lanes.innerHTML = html; });
  }

  var es = new EventSource("/events");
  es.onmessage = function (e) {
    var ev;
    try { ev = JSON.parse(e.data); } catch (_) { return; }

    if (ev.status === "running") {
      setBusy(true);
      var pct = ev.total > 0 ? Math.round((100 * ev.done) / ev.total) : 0;
      bar.style.width = pct + "%";
      var label = ev.phase || "working";
      if (ev.repo) label += " · " + ev.repo;
      if (ev.total > 0) label += " (" + ev.done + "/" + ev.total + ")";
      status.textContent = label;
      if (ev.tokens_in || ev.tokens_out) {
        tokens.textContent = "⛃ " + fmtTokens(ev.tokens_in) + " in / " + fmtTokens(ev.tokens_out) + " out";
      }
    } else if (ev.status === "done") {
      bar.style.width = "100%";
      status.textContent = "done";
      setBusy(false);
      reloadLanes();
      setTimeout(function () { bar.style.width = "0%"; status.textContent = ""; }, 1500);
    } else if (ev.status === "error") {
      status.textContent = "error: " + (ev.message || "unknown");
      setBusy(false);
    }
  };

  // Copy buttons on draft-comment cards. Delegated on document so it keeps
  // working after the lanes fragment is swapped in.
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest(".copy-btn");
    if (!b) return;
    var src = document.getElementById(b.dataset.t);
    if (!src) return;
    var txt = src.textContent;
    function ok() {
      var prev = b.dataset.p || b.textContent;
      b.dataset.p = prev;
      b.textContent = "Copied ✓";
      b.classList.add("ok");
      setTimeout(function () { b.textContent = prev; b.classList.remove("ok"); }, 1200);
    }
    function fallback() {
      var ta = document.createElement("textarea");
      ta.value = txt;
      ta.style.position = "fixed"; ta.style.top = "0"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.focus(); ta.select();
      try { document.execCommand("copy"); ok(); } catch (_) { b.textContent = "Copy failed"; }
      document.body.removeChild(ta);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(txt).then(ok).catch(fallback);
    } else {
      fallback();
    }
  });

  refresh.addEventListener("click", function () {
    status.textContent = "starting…"; bar.style.width = "0%"; tokens.textContent = "";
    fetch("/refresh", { method: "POST" });
  });
  dispatch.addEventListener("click", function () {
    status.textContent = "starting…"; bar.style.width = "0%"; tokens.textContent = "";
    fetch("/dispatch", { method: "POST" });
  });
  cancel.addEventListener("click", function () {
    status.textContent = "cancelling…";
    fetch("/cancel", { method: "POST" });
  });
})();
