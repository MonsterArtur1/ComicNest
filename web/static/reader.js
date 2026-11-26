// ComicNest page reader: one page at a time, zoom, keyboard/touch paging,
// guided view (panel by panel), progress reported to the server (same record
// OPDS readers use).
(function () {
    "use strict";

    const root = document.getElementById("reader");
    const issueID = root.dataset.issue;
    const total = parseInt(root.dataset.total, 10);
    const nextIssue = root.dataset.next !== "0" ? root.dataset.next : null;

    const stage = document.getElementById("stage");
    const img = document.getElementById("page");
    const slider = document.getElementById("slider");
    const pageNum = document.getElementById("page-num");
    const zoomLabel = document.getElementById("zoom-label");
    const loading = document.getElementById("loading");
    const guidedBtn = document.getElementById("guided");
    const panelMask = document.getElementById("panel-mask");
    const panelBadge = document.getElementById("panel-badge");

    // Guided view state (see the "guided view" section below); declared here
    // because applyZoom needs it.
    const GUIDED_KEY = "comicnest.reader.guided";
    let guided = false;
    try { guided = localStorage.getItem(GUIDED_KEY) === "1"; } catch (e) { /* ignore */ }

    // --- zoom -----------------------------------------------------------
    // mode: "fit-h" (whole page visible), "fit-w" (page width = viewport,
    // scroll vertically) or "custom" (scale in percent, scroll both ways).
    const ZOOM_KEY = "comicnest.reader.zoom";
    let zoom = { mode: "fit-h", scale: 100 };
    try {
        const saved = JSON.parse(localStorage.getItem(ZOOM_KEY));
        if (saved && saved.mode) zoom = saved;
    } catch (e) { /* ignore */ }

    function applyZoom() {
        // Guided view always lays the page out to fit the screen; panels are
        // then zoomed into with a transform. The saved zoom stays untouched.
        const layout = guided ? "fit-h" : zoom.mode;
        root.classList.remove("zoom-fit-h", "zoom-fit-w", "zoom-custom");
        root.classList.add("zoom-" + layout);
        if (layout === "custom") {
            img.style.width = (img.naturalWidth * zoom.scale / 100) + "px";
            zoomLabel.textContent = zoom.scale + "%";
        } else {
            img.style.width = "";
            zoomLabel.textContent = guided ? "panel" : zoom.mode === "fit-h" ? "auto" : "width";
        }
        document.querySelectorAll("[data-zoom]").forEach(function (b) {
            b.classList.toggle("active", !guided && b.dataset.zoom === zoom.mode);
        });
        try { localStorage.setItem(ZOOM_KEY, JSON.stringify(zoom)); } catch (e) { /* ignore */ }
    }

    function setZoom(action) {
        if (guided) setGuided(false); // zooming by hand leaves guided view
        if (action === "fit-h" || action === "fit-w") {
            zoom.mode = action;
        } else {
            // Entering custom mode starts from the size the page has now.
            if (zoom.mode !== "custom") {
                zoom.scale = Math.round(img.clientWidth / img.naturalWidth * 100) || 100;
                zoom.mode = "custom";
            }
            zoom.scale = action === "in"
                ? Math.min(400, zoom.scale + 20)
                : Math.max(20, zoom.scale - 20);
        }
        applyZoom();
    }

    // --- paging ---------------------------------------------------------
    let current = Math.min(Math.max(parseInt(root.dataset.start, 10) || 1, 1), total); // 1-based
    const cache = {};

    function pageURL(n) {
        return "/issues/" + issueID + "/pages/" + (n - 1) + "?track=0";
    }

    function preload(n) {
        if (n < 1 || n > total || cache[n]) return;
        const i = new Image();
        i.src = pageURL(n);
        cache[n] = i;
    }

    let progressTimer = null;
    function reportProgress(immediate) {
        clearTimeout(progressTimer);
        const send = function () {
            const body = new URLSearchParams({ page: String(current) });
            if (immediate && navigator.sendBeacon) {
                navigator.sendBeacon("/issues/" + issueID + "/progress", body);
            } else {
                fetch("/issues/" + issueID + "/progress", { method: "POST", body: body, keepalive: true })
                    .catch(function () { /* offline: progress is best effort */ });
            }
        };
        if (immediate) send(); else progressTimer = setTimeout(send, 400);
    }

    // enter = "last" opens the page on its last panel (guided view, paging
    // backwards); anything else on its first.
    function show(n, enter) {
        n = Math.min(Math.max(n, 1), total);
        current = n;
        loadedPage = 0;
        wholePage = false;
        panelIdx = enter === "last" ? Infinity : 0; // clamped once panels are known
        if (guided) { loadPanels(n); loadPanels(n + 1); loadPanels(n - 1); }
        loading.hidden = false;
        img.src = pageURL(n);
        pageNum.textContent = n;
        slider.value = n;
        document.title = "p. " + n + "/" + total + " — " + root.querySelector(".reader-title").textContent;
        stage.scrollTop = 0;
        stage.scrollLeft = 0;
        preload(n + 1);
        preload(n - 1);
        reportProgress(false);
    }

    img.addEventListener("load", function () {
        loading.hidden = true;
        loadedPage = current;
        applyZoom();
        placeGuided(true);
    });
    img.addEventListener("error", function () {
        loading.hidden = false;
        loading.textContent = "Failed to load page " + current;
        placeGuided(true); // drop the previous page's panel zoom
    });

    // Past the last page nothing happens — the reader leaves when they want
    // (Esc / back arrow / "next issue" on the bottom bar). In guided view the
    // panels of a page come first, then the next page's first panel.
    function next() {
        if (guidedActive() && panelIdx < panelData[current].length - 1) {
            panelIdx++;
            placeGuided(false);
            return;
        }
        if (current < total) show(current + 1);
    }
    function prev() {
        if (guidedActive() && panelIdx > 0) {
            panelIdx--;
            placeGuided(false);
            return;
        }
        if (current > 1) show(current - 1, "last");
    }

    // --- guided view ------------------------------------------------------
    // Panel rectangles (normalized 0..1) come from the server per page and
    // are cached here; a page with one full-page panel, or whose request
    // failed, reads like a normal page. The page keeps its fit-to-screen
    // layout and is zoomed onto the current panel with a CSS transform (the
    // transition is in the stylesheet, off for prefers-reduced-motion); a
    // mask dims the neighbouring panels. A new page opens straight on its
    // first (or, paging back, last) panel without animating. Progress stays
    // per page — panels are never reported.
    let loadedPage = 0;   // page the <img> currently shows (0 = loading)
    let panelIdx = 0;     // current panel on the current page
    let wholePage = false; // temporarily showing the whole page ("v", double-tap)
    const panelData = {};  // page -> [rect...] once fetched ([] = unusable)
    const panelReq = {};

    function loadPanels(n) {
        if (n < 1 || n > total || panelReq[n]) return;
        panelReq[n] = fetch("/issues/" + issueID + "/pages/" + (n - 1) + "/panels")
            .then(function (r) { return r.ok ? r.json() : null; })
            .then(function (d) { return d && Array.isArray(d.panels) ? d.panels : null; })
            .catch(function () { return null; })
            .then(function (ps) {
                panelData[n] = ps && ps.length > 1 ? ps : [];
                if (n === current) placeGuided(false);
            });
    }

    function hasPanels() {
        const ps = panelData[current];
        return guided && !!ps && ps.length > 1;
    }

    // guidedActive: the current page is being shown panel by panel.
    function guidedActive() {
        return hasPanels() && !wholePage && loadedPage === current && img.naturalWidth > 0;
    }

    // placeGuided zooms onto the current panel (or back to the whole page);
    // instant skips the transition (page changes, resizes).
    function placeGuided(instant) {
        const active = guidedActive();
        root.classList.toggle("guided-active", active);
        if (instant) root.classList.add("guided-instant");

        const ps = panelData[current];
        if (active) panelIdx = Math.min(Math.max(panelIdx, 0), ps.length - 1);
        panelBadge.hidden = !hasPanels();
        if (hasPanels()) {
            panelBadge.textContent = wholePage
                ? "whole page · " + ps.length + " panels"
                : "panel " + (Math.min(panelIdx, ps.length - 1) + 1) + "/" + ps.length;
        }

        if (!active) {
            img.style.transform = "";
            panelMask.hidden = true;
        } else {
            const p = ps[panelIdx];
            const W = stage.clientWidth, H = stage.clientHeight;
            const L = img.offsetLeft, T = img.offsetTop;
            const iw = img.clientWidth, ih = img.clientHeight;
            const pw = p.w * iw, ph = p.h * ih;
            // The page already fits the screen, so a panel only ever zooms in
            // (capped, tiny panels would just get blurry).
            const s = Math.min(5, Math.max(1, Math.min(W * 0.96 / pw, H * 0.96 / ph)));
            const cx = L + (p.x + p.w / 2) * iw, cy = T + (p.y + p.h / 2) * ih;
            // transform-origin is the image's top-left corner (L, T).
            const tx = W / 2 - L - s * (cx - L), ty = H / 2 - T - s * (cy - T);
            img.style.transform = "translate(" + tx + "px, " + ty + "px) scale(" + s + ")";
            const mw = s * pw + 8, mh = s * ph + 8;
            panelMask.style.left = (W - mw) / 2 + "px";
            panelMask.style.top = (H - mh) / 2 + "px";
            panelMask.style.width = mw + "px";
            panelMask.style.height = mh + "px";
            panelMask.hidden = false;
            stage.scrollTop = 0;
            stage.scrollLeft = 0;
        }

        if (instant) {
            void img.offsetWidth; // commit the jump before transitions return
            requestAnimationFrame(function () {
                requestAnimationFrame(function () { root.classList.remove("guided-instant"); });
            });
        }
    }

    function setGuided(on) {
        guided = on;
        try { localStorage.setItem(GUIDED_KEY, on ? "1" : "0"); } catch (e) { /* ignore */ }
        root.classList.toggle("guided-on", on);
        guidedBtn.classList.toggle("active", on);
        guidedBtn.setAttribute("aria-pressed", on ? "true" : "false");
        wholePage = false;
        if (on) { loadPanels(current); loadPanels(current + 1); loadPanels(current - 1); }
        applyZoom();
        placeGuided(true);
    }

    function toggleWholePage() {
        if (!hasPanels()) return;
        wholePage = !wholePage;
        placeGuided(false);
    }

    // --- chrome (bars) --------------------------------------------------
    let hideTimer = null;
    function showBars() {
        root.classList.remove("bars-hidden");
        clearTimeout(hideTimer);
        hideTimer = setTimeout(function () { root.classList.add("bars-hidden"); }, 2500);
    }
    function toggleBars() {
        if (root.classList.contains("bars-hidden")) showBars();
        else { clearTimeout(hideTimer); root.classList.add("bars-hidden"); }
    }

    // --- events ---------------------------------------------------------
    // A double tap on the middle zone shows the whole page in guided view;
    // its first tap already toggled the bars, so the second one undoes that.
    let lastMenuTap = 0;
    root.addEventListener("click", function (e) {
        const el = e.target.closest("[data-nav], [data-zoom]");
        if (!el) return;
        if (el.dataset.zoom) { setZoom(el.dataset.zoom); showBars(); return; }
        switch (el.dataset.nav) {
            case "next": next(); break;
            case "prev": prev(); break;
            case "menu": {
                const now = Date.now();
                if (hasPanels() && now - lastMenuTap < 350) {
                    lastMenuTap = 0;
                    toggleBars();
                    toggleWholePage();
                } else {
                    lastMenuTap = now;
                    toggleBars();
                }
                break;
            }
        }
    });

    guidedBtn.addEventListener("click", function () { setGuided(!guided); showBars(); });
    panelBadge.addEventListener("click", toggleWholePage);
    window.addEventListener("resize", function () { if (guided) placeGuided(true); });

    slider.addEventListener("input", function () { show(parseInt(slider.value, 10)); showBars(); });

    document.getElementById("fullscreen").addEventListener("click", function () {
        if (document.fullscreenElement) document.exitFullscreen();
        else root.requestFullscreen && root.requestFullscreen();
    });

    document.addEventListener("keydown", function (e) {
        if (e.target.tagName === "INPUT" && e.target !== slider) return;
        switch (e.key) {
            case "ArrowRight": case "PageDown": case " ": case "d": e.preventDefault(); next(); break;
            case "ArrowLeft": case "PageUp": case "a": e.preventDefault(); prev(); break;
            case "Home": show(1); break;
            case "End": show(total); break;
            case "+": case "=": setZoom("in"); break;
            case "-": case "_": setZoom("out"); break;
            case "0": setZoom("fit-h"); break;
            case "w": setZoom("fit-w"); break;
            case "f": document.getElementById("fullscreen").click(); break;
            case "g": setGuided(!guided); break;
            case "v": toggleWholePage(); break;
            case "Escape":
                if (document.fullscreenElement) break; // browser handles it
                reportProgress(true);
                window.location.href = root.dataset.back;
                break;
            default: return;
        }
        showBars();
    });

    // Wheel: with the whole page visible (or in guided view) it turns pages /
    // panels; otherwise it scrolls.
    stage.addEventListener("wheel", function (e) {
        if (e.ctrlKey) { e.preventDefault(); setZoom(e.deltaY < 0 ? "in" : "out"); return; }
        const scrollable = stage.scrollHeight > stage.clientHeight + 2;
        if (!scrollable || guided) {
            e.preventDefault();
            if (e.deltaY > 0) next(); else if (e.deltaY < 0) prev();
        }
    }, { passive: false });

    let touchX = null, touchY = null;
    stage.addEventListener("touchstart", function (e) {
        touchX = e.changedTouches[0].clientX; touchY = e.changedTouches[0].clientY;
    }, { passive: true });
    stage.addEventListener("touchend", function (e) {
        if (touchX === null) return;
        const dx = e.changedTouches[0].clientX - touchX;
        const dy = e.changedTouches[0].clientY - touchY;
        touchX = touchY = null;
        if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy) * 1.5) {
            if (dx < 0) next(); else prev();
        }
    }, { passive: true });

    document.addEventListener("mousemove", showBars);
    window.addEventListener("pagehide", function () { reportProgress(true); });
    document.addEventListener("visibilitychange", function () {
        if (document.visibilityState === "hidden") reportProgress(true);
    });

    // --- go ---------------------------------------------------------------
    setGuided(guided); // applies the zoom layout too
    show(current);
    showBars();
    if (nextIssue) {
        // Warm the next issue's first page so "Next issue" opens instantly.
        const warm = new Image();
        warm.src = "/issues/" + nextIssue + "/pages/0?track=0";
    }
})();
