(function (window) {
    function msg(text) {
        var el = document.createElement("div");
        el.className = "layui-layer-msg";
        el.textContent = text || "";
        document.body.appendChild(el);
        window.setTimeout(function () {
            if (el.parentNode) {
                el.parentNode.removeChild(el);
            }
        }, 2200);
    }

    window.layui = {
        use: function (_, callback) {
            if (typeof callback === "function") {
                callback();
            }
        },
        layer: {
            msg: msg,
            confirm: function (text, options, yes) {
                if (typeof options === "function") {
                    yes = options;
                }
                if (window.confirm(text) && typeof yes === "function") {
                    yes();
                }
            }
        },
        form: {
            render: function () {}
        }
    };
})(window);
