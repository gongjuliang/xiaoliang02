(function ($, layui) {
    var apiBase = "/api/server/v1";
    var tokenKey = "natt_server_access_token";
    var refreshKey = "natt_server_refresh_token";
    var state = { view: "dashboard", pages: {} };
    var titles = {
        dashboard: ["仪表盘", "运行概览"],
        clients: ["客户端", "授权与在线状态"],
        tunnels: ["隧道", "TCP 端口穿透"],
        config: ["配置", "热加载与重启项"],
        audit: ["审计", "管理操作记录"]
    };

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
        var text = escapeHtml(value || "-");
        var lower = String(value || "").toLowerCase();
        var cls = "";
        if (["online", "enabled", "running", "ok"].indexOf(lower) >= 0) {
            cls = " ok";
        } else if (["offline", "stopped", "connecting"].indexOf(lower) >= 0) {
            cls = " warn";
        } else if (["disabled", "error"].indexOf(lower) >= 0) {
            cls = " err";
        }
        return '<span class="badge' + cls + '">' + text + "</span>";
    }

    function request(method, path, data) {
        return $.ajax({
            url: apiBase + path,
            method: method,
            data: data == null ? undefined : JSON.stringify(data),
            contentType: data == null ? undefined : "application/json",
            headers: token() ? { Authorization: "Bearer " + token() } : {}
        }).then(function (resp) {
            if (resp.code !== 0) {
                return $.Deferred().reject(resp).promise();
            }
            return resp.data;
        }, function (xhr) {
            var resp = xhr.responseJSON || { message: xhr.statusText || "请求失败" };
            if (xhr.status === 401 && path !== "/auth/login") {
                logout();
            }
            return $.Deferred().reject(resp).promise();
        });
    }

    function setView(view) {
        state.view = view;
        $("#nav button").removeClass("active");
        $('#nav button[data-view="' + view + '"]').addClass("active");
        $("#viewTitle").text(titles[view][0]);
        $("#viewSubTitle").text(titles[view][1]);
        render();
    }

    function showLogin() {
        $("#loginView").removeClass("hidden");
        $("#appView").addClass("hidden");
    }

    function showApp() {
        $("#loginView").addClass("hidden");
        $("#appView").removeClass("hidden");
        setView(state.view || "dashboard");
    }

    function logout() {
        localStorage.removeItem(tokenKey);
        localStorage.removeItem(refreshKey);
        showLogin();
    }

    function render() {
        if (!token()) {
            showLogin();
            return;
        }
        if (state.view === "dashboard") {
            renderDashboard();
        } else if (state.view === "clients") {
            renderClients();
        } else if (state.view === "tunnels") {
            renderTunnels();
        } else if (state.view === "config") {
            renderConfig();
        } else if (state.view === "audit") {
            renderAudit();
        }
    }

    function statGrid(items) {
        return '<div class="stat-grid">' + items.map(function (item) {
            return '<div class="stat-card"><span>' + escapeHtml(item.label) + '</span><strong>' + escapeHtml(item.value) + '</strong></div>';
        }).join("") + "</div>";
    }

    function table(columns, items, actions) {
        if (!items || items.length === 0) {
            return '<div class="table-wrap"><div class="empty">暂无数据</div></div>';
        }
        var head = columns.map(function (col) {
            return "<th>" + escapeHtml(col.title) + "</th>";
        }).join("") + (actions ? "<th>操作</th>" : "");
        var rows = items.map(function (item) {
            var cells = columns.map(function (col) {
                var value = typeof col.render === "function" ? col.render(item) : escapeHtml(item[col.key]);
                return "<td>" + value + "</td>";
            }).join("");
            if (actions) {
                cells += '<td><div class="row-actions">' + actions(item) + "</div></td>";
            }
            return "<tr>" + cells + "</tr>";
        }).join("");
        return '<div class="table-wrap"><table class="data-table"><thead><tr>' + head + "</tr></thead><tbody>" + rows + "</tbody></table></div>";
    }

    function pageState(view) {
        if (!state.pages[view]) {
            state.pages[view] = { page: 1, page_size: 20 };
        }
        return state.pages[view];
    }

    function pageQuery(view) {
        var p = pageState(view);
        return "page=" + encodeURIComponent(p.page) + "&page_size=" + encodeURIComponent(p.page_size);
    }

    function renderPager(view, page) {
        var p = pageState(view);
        p.page = Number(page.page || p.page || 1);
        p.page_size = Number(page.page_size || p.page_size || 20);
        var total = Number(page.total || 0);
        var totalPages = Math.max(1, Math.ceil(total / p.page_size));
        if (p.page > totalPages) {
            p.page = totalPages;
        }
        return '<div class="pager" data-view="' + escapeHtml(view) + '">' +
            '<span>共 ' + escapeHtml(total) + ' 条</span>' +
            '<button class="layui-btn secondary" data-page-action="prev" type="button"' + (p.page <= 1 ? " disabled" : "") + '>上一页</button>' +
            '<strong>第 ' + escapeHtml(p.page) + ' / ' + escapeHtml(totalPages) + ' 页</strong>' +
            '<button class="layui-btn secondary" data-page-action="next" data-total-pages="' + totalPages + '" type="button"' + (p.page >= totalPages ? " disabled" : "") + '>下一页</button>' +
            '<select class="inline-input pager-size" data-page-action="size">' +
            [10, 20, 50, 100].map(function (size) {
                return '<option value="' + size + '"' + (size === p.page_size ? " selected" : "") + '>' + size + ' / 页</option>';
            }).join("") +
            '</select>' +
            '</div>';
    }

    function actionButton(action, id, text, style) {
        return '<button class="layui-btn ' + (style || "secondary") + '" data-action="' + action + '" data-id="' + id + '" type="button">' + text + "</button>";
    }

    function renderDashboard() {
        $("#content").html('<div class="empty">加载中</div>');
        request("GET", "/dashboard").then(function (data) {
            $("#content").html(
                statGrid([
                    { label: "客户端总数", value: data.total_clients || 0 },
                    { label: "在线客户端", value: data.online_clients || 0 },
                    { label: "隧道总数", value: data.total_tunnels || 0 },
                    { label: "运行隧道", value: data.running_tunnels || 0 }
                ]) +
                '<div class="panel"><h3>流量</h3><p class="muted">入站 ' + escapeHtml(data.total_bytes_in || 0) + ' bytes / 出站 ' + escapeHtml(data.total_bytes_out || 0) + ' bytes</p></div>'
            );
        }).fail(showError);
    }

    function renderClients() {
        $("#content").html('<div class="empty">加载中</div>');
        request("GET", "/clients?" + pageQuery("clients")).then(function (page) {
            var items = page.items || [];
            $("#content").html(
                '<div class="toolbar"><h3>客户端列表</h3><button id="addClientBtn" class="layui-btn primary" type="button">新增客户端</button></div>' +
                table([
                    { title: "ID", key: "id" },
                    { title: "名称", key: "name" },
                    { title: "状态", render: function (x) { return badge(x.status); } },
                    { title: "在线", render: function (x) { return badge(x.online_status); } },
                    { title: "秘钥", key: "secret_hint" },
                    { title: "备注", key: "remark" }
                ], items, function (x) {
                    return actionButton("client-edit", x.id, "编辑") +
                        actionButton(x.status === "disabled" ? "client-enable" : "client-disable", x.id, x.status === "disabled" ? "启用" : "禁用") +
                        actionButton("client-rotate", x.id, "轮换");
                }) +
                renderPager("clients", page)
            );
        }).fail(showError);
    }

    function renderTunnels() {
        $("#content").html('<div class="empty">加载中</div>');
        $.when(request("GET", "/tunnels?" + pageQuery("tunnels")), request("GET", "/clients?page=1&page_size=100")).then(function (tunnelPage, clientPage) {
            var items = tunnelPage.items || [];
            state.clients = clientPage.items || [];
            $("#content").html(
                '<div class="toolbar"><h3>隧道列表</h3><button id="addTunnelBtn" class="layui-btn primary" type="button">新增隧道</button></div>' +
                table([
                    { title: "ID", key: "id" },
                    { title: "名称", key: "name" },
                    { title: "客户端", key: "client_id" },
                    { title: "本地", render: function (x) { return escapeHtml(x.local_host + ":" + x.local_port); } },
                    { title: "公网", render: function (x) { return escapeHtml(x.remote_host + ":" + x.remote_port); } },
                    { title: "状态", render: function (x) { return badge(x.status); } },
                    { title: "自启动", render: function (x) { return x.auto_start ? "是" : "否"; } }
                ], items, function (x) {
                    return actionButton("tunnel-edit", x.id, "编辑") +
                        actionButton("tunnel-start", x.id, "启动") +
                        actionButton("tunnel-stop", x.id, "停止") +
                        actionButton("tunnel-delete", x.id, "删除", "danger");
                }) +
                renderPager("tunnels", tunnelPage)
            );
        }).fail(showError);
    }

    function renderConfig() {
        $("#content").html('<div class="empty">加载中</div>');
        request("GET", "/config").then(function (data) {
            var keys = data.editable_keys || [];
            $("#content").html(
                '<div class="panel"><div class="toolbar"><h3>配置修改</h3><button id="saveConfigBtn" class="layui-btn primary" type="button">保存</button></div>' +
                '<label class="field"><span>配置项</span><select id="configKey">' + keys.map(function (x) {
                    return '<option value="' + escapeHtml(x.key) + '">' + escapeHtml(x.key) + (x.hot_reload ? " / hot" : " / restart") + "</option>";
                }).join("") + '</select></label><label class="field"><span>值</span><input id="configValue"></label></div>' +
                '<div class="panel"><h3>当前配置</h3><pre>' + escapeHtml(JSON.stringify(data.current || {}, null, 2)) + "</pre></div>"
            );
        }).fail(showError);
    }

    function renderAudit() {
        $("#content").html('<div class="empty">加载中</div>');
        request("GET", "/audit-logs?" + pageQuery("audit")).then(function (page) {
            $("#content").html(table([
                { title: "时间", key: "created_at" },
                { title: "操作者", key: "actor" },
                { title: "动作", key: "action" },
                { title: "目标", render: function (x) { return escapeHtml((x.target_type || "") + " " + (x.target_id || "")); } },
                { title: "内容", key: "content" },
                { title: "IP", key: "ip" }
            ], page.items || []) + renderPager("audit", page));
        }).fail(showError);
    }

    function openForm(title, fields, onSubmit) {
        var html = '<div class="modal-panel"><div class="modal-head"><strong>' + escapeHtml(title) + '</strong><button class="layui-btn ghost" data-modal-close type="button">关闭</button></div><form id="modalForm"><div class="modal-body">';
        fields.forEach(function (f) {
            html += '<label class="field"><span>' + escapeHtml(f.label) + '</span>';
            if (f.type === "select") {
                html += '<select name="' + escapeHtml(f.name) + '">' + (f.options || []).map(function (opt) {
                    return '<option value="' + escapeHtml(opt.value) + '"' + (String(opt.value) === String(f.value) ? " selected" : "") + ">" + escapeHtml(opt.label) + "</option>";
                }).join("") + "</select>";
            } else if (f.type === "textarea") {
                html += '<textarea name="' + escapeHtml(f.name) + '">' + escapeHtml(f.value || "") + "</textarea>";
            } else {
                html += '<input name="' + escapeHtml(f.name) + '" type="' + escapeHtml(f.type || "text") + '" value="' + escapeHtml(f.value || "") + '">';
            }
            html += "</label>";
        });
        html += '</div><div class="modal-foot"><button class="layui-btn secondary" data-modal-close type="button">取消</button><button class="layui-btn primary" type="submit">保存</button></div></form></div>';
        $("#modalRoot").html(html).removeClass("hidden");
        $("#modalForm").on("submit", function (e) {
            e.preventDefault();
            var data = {};
            fields.forEach(function (f) {
                data[f.name] = $('[name="' + f.name + '"]', "#modalForm").val();
            });
            onSubmit(data);
        });
    }

    function closeModal() {
        $("#modalRoot").addClass("hidden").empty();
    }

    function showError(err) {
        layui.layer.msg(err && err.message ? err.message : "请求失败");
    }

    function clientForm(client) {
        openForm(client ? "编辑客户端" : "新增客户端", [
            { name: "name", label: "名称", value: client && client.name },
            { name: "remark", label: "备注", type: "textarea", value: client && client.remark }
        ], function (data) {
            request(client ? "PUT" : "POST", client ? "/clients/" + client.id : "/clients", data).then(function (resp) {
                closeModal();
                if (resp.client_secret) {
                    layui.layer.msg("客户端秘钥: " + resp.client_secret);
                }
                renderClients();
            }).fail(showError);
        });
    }

    function tunnelForm(tunnel) {
        var clientOptions = (state.clients || []).map(function (client) {
            return { value: client.id, label: client.name + " #" + client.id };
        });
        openForm(tunnel ? "编辑隧道" : "新增隧道", [
            { name: "name", label: "名称", value: tunnel && tunnel.name },
            { name: "client_id", label: "客户端", type: "select", value: tunnel && tunnel.client_id, options: clientOptions },
            { name: "local_host", label: "本地地址", value: tunnel && tunnel.local_host || "127.0.0.1" },
            { name: "local_port", label: "本地端口", type: "number", value: tunnel && tunnel.local_port },
            { name: "remote_host", label: "公网监听地址", value: tunnel && tunnel.remote_host || "0.0.0.0" },
            { name: "remote_port", label: "公网监听端口", type: "number", value: tunnel && tunnel.remote_port },
            { name: "auto_start", label: "自启动 true/false", value: tunnel ? String(!!tunnel.auto_start) : "false" },
            { name: "remark", label: "备注", type: "textarea", value: tunnel && tunnel.remark }
        ], function (data) {
            data.client_id = Number(data.client_id);
            data.local_port = Number(data.local_port);
            data.remote_port = Number(data.remote_port);
            data.auto_start = data.auto_start === "true";
            request(tunnel ? "PUT" : "POST", tunnel ? "/tunnels/" + tunnel.id : "/tunnels", data).then(function () {
                closeModal();
                renderTunnels();
            }).fail(showError);
        });
    }

    $("#loginForm").on("submit", function (e) {
        e.preventDefault();
        $("#loginError").text("");
        request("POST", "/auth/login", {
            username: $('[name="username"]', this).val(),
            password: $('[name="password"]', this).val()
        }).then(function (tokens) {
            localStorage.setItem(tokenKey, tokens.access_token);
            localStorage.setItem(refreshKey, tokens.refresh_token);
            showApp();
        }).fail(function (err) {
            $("#loginError").text(err && err.message ? err.message : "登录失败");
        });
    });

    $("#nav").on("click", "button", function () {
        setView($(this).data("view"));
    });
    $("#refreshBtn").on("click", render);
    $("#logoutBtn").on("click", logout);
    $("#content").on("click", "#addClientBtn", function () { clientForm(null); });
    $("#content").on("click", "#addTunnelBtn", function () { tunnelForm(null); });
    $("#content").on("click", "#saveConfigBtn", function () {
        var key = $("#configKey").val();
        var value = $("#configValue").val();
        request("PUT", "/config", { settings: (function () { var obj = {}; obj[key] = value; return obj; })() }).then(function () {
            layui.layer.msg("已保存");
            renderConfig();
        }).fail(showError);
    });
    $("#content").on("click", "[data-page-action]", function () {
        var action = $(this).data("page-action");
        if (action === "size") {
            return;
        }
        var pager = $(this).closest(".pager");
        var view = pager.data("view");
        var p = pageState(view);
        var totalPages = Number($(this).data("total-pages") || 1);
        if (action === "prev" && p.page > 1) {
            p.page -= 1;
        } else if (action === "next" && p.page < totalPages) {
            p.page += 1;
        }
        render();
    });
    $("#content").on("change", 'select[data-page-action="size"]', function () {
        var view = $(this).closest(".pager").data("view");
        var p = pageState(view);
        p.page = 1;
        p.page_size = Number($(this).val());
        render();
    });
    $("#content").on("click", "[data-action]", function () {
        var action = $(this).data("action");
        var id = $(this).data("id");
        if (action === "client-enable" || action === "client-disable" || action === "client-rotate") {
            request("POST", "/clients/" + id + "/" + (action === "client-enable" ? "enable" : action === "client-disable" ? "disable" : "rotate-secret")).then(function (resp) {
                if (resp.client_secret) {
                    layui.layer.msg("客户端秘钥: " + resp.client_secret);
                }
                renderClients();
            }).fail(showError);
        } else if (action === "client-edit") {
            request("GET", "/clients?page=1&page_size=100").then(function (page) {
                clientForm((page.items || []).filter(function (x) { return x.id === id; })[0]);
            }).fail(showError);
        } else if (action === "tunnel-start" || action === "tunnel-stop") {
            request("POST", "/tunnels/" + id + "/" + (action === "tunnel-start" ? "start" : "stop")).then(renderTunnels).fail(showError);
        } else if (action === "tunnel-delete") {
            layui.layer.confirm("删除该隧道？", function () {
                request("DELETE", "/tunnels/" + id).then(renderTunnels).fail(showError);
            });
        } else if (action === "tunnel-edit") {
            request("GET", "/tunnels?page=1&page_size=100").then(function (page) {
                tunnelForm((page.items || []).filter(function (x) { return x.id === id; })[0]);
            }).fail(showError);
        }
    });
    $("#modalRoot").on("click", "[data-modal-close]", closeModal);

    if (token()) {
        showApp();
    } else {
        showLogin();
    }
})(jQuery, layui);
