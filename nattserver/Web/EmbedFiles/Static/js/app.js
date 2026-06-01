(function ($) {
    var apiBase = "/api/server/v1";
    var tokenKey = "natt_server_access_token";
    var refreshKey = "natt_server_refresh_token";

    function token() {
        return localStorage.getItem(tokenKey) || "";
    }

    function escapeHtml(value) {
        return String(value == null ? "" : value)
            .replace(/&/g, "&amp;")
            .replace(/</g, "&lt;")
            .replace(/>/g, "&gt;")
            .replace(/"/g, "&quot;")
            .replace(/'/g, "&#39;");
    }

    function badge(value) {
        var lower = String(value || "").toLowerCase();
        var cls = "";
        if (["online", "enabled", "running", "ok", "true"].indexOf(lower) >= 0) cls = " ok";
        if (["offline", "stopped", "connecting", "false"].indexOf(lower) >= 0) cls = " warn";
        if (["disabled", "error"].indexOf(lower) >= 0) cls = " err";
        return '<span class="badge' + cls + '">' + escapeHtml(value || "-") + "</span>";
    }

    function request(method, path, data) {
        return $.ajax({
            url: apiBase + path,
            method: method,
            data: data == null ? undefined : JSON.stringify(data),
            contentType: data == null ? undefined : "application/json",
            headers: token() ? { Authorization: "Bearer " + token() } : {}
        }).then(function (resp) {
            if (resp.code !== 0) return $.Deferred().reject(resp).promise();
            return resp.data;
        }, function (xhr) {
            var resp = xhr.responseJSON || { message: xhr.statusText || "请求失败" };
            if (xhr.status === 401 && path !== "/auth/login") logout();
            return $.Deferred().reject(resp).promise();
        });
    }

    function showError(err) {
        var message = err && err.message ? err.message : "操作失败";
        if (window.layui && layui.layer) layui.layer.msg(message, { icon: 2 });
        else alert(message);
    }

    function logout() {
        localStorage.removeItem(tokenKey);
        localStorage.removeItem(refreshKey);
        window.top.location.href = "/login.html";
    }

    function requireAuth() {
        if (!token()) {
            window.top.location.href = "/login.html";
            return false;
        }
        return true;
    }

    $("#loginForm").on("submit", function (e) {
        e.preventDefault();
        var payload = {
            username: $.trim($('[name="username"]').val()),
            password: $('[name="password"]').val()
        };
        request("POST", "/auth/login", payload).then(function (data) {
            localStorage.setItem(tokenKey, data.access_token || "");
            localStorage.setItem(refreshKey, data.refresh_token || "");
            window.location.href = "/index.html";
        }).fail(function (err) {
            $("#loginError").text(err.message || "登录失败");
        });
    });

    window.NATT = {
        request: request,
        escapeHtml: escapeHtml,
        badge: badge,
        showError: showError,
        logout: logout,
        requireAuth: requireAuth,
        token: token
    };
})(jQuery);
