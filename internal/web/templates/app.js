(function () {
  var btn = document.getElementById("refresh");
  var bar = document.getElementById("bar");
  var status = document.getElementById("status");
  var lanes = document.getElementById("lanes");

  var es = new EventSource("/events");
  es.onmessage = function (e) {
    var ev;
    try { ev = JSON.parse(e.data); } catch (_) { return; }

    if (ev.status === "running") {
      btn.disabled = true;
      var pct = ev.total > 0 ? Math.round((100 * ev.done) / ev.total) : 0;
      bar.style.width = pct + "%";
      var label = ev.phase || "working";
      if (ev.repo) label += " · " + ev.repo;
      if (ev.total > 0) label += " (" + ev.done + "/" + ev.total + ")";
      status.textContent = label;
    } else if (ev.status === "done") {
      bar.style.width = "100%";
      status.textContent = "done";
      btn.disabled = false;
      fetch("/lanes")
        .then(function (r) { return r.text(); })
        .then(function (html) {
          lanes.innerHTML = html;
          setTimeout(function () { bar.style.width = "0%"; status.textContent = ""; }, 1500);
        });
    } else if (ev.status === "error") {
      status.textContent = "error: " + (ev.message || "unknown");
      btn.disabled = false;
    }
  };

  btn.addEventListener("click", function () {
    status.textContent = "starting…";
    bar.style.width = "0%";
    fetch("/refresh", { method: "POST" });
  });
})();
