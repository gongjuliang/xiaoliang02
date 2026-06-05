(function (global) {
    "use strict";

    var P = BigInt("0xfffffffeffffffffffffffffffffffffffffffff00000000ffffffffffffffff");
    var A = BigInt("0xfffffffeffffffffffffffffffffffffffffffff00000000fffffffffffffffc");
    var B = BigInt("0x28e9fa9e9d9f5e344d5a9e4bcf6509a7f39789f515ab8f92ddbcbd414d940e93");
    var N = BigInt("0xfffffffeffffffffffffffffffffffff7203df6b21c6052b53bbf40939d54123");
    var GX = BigInt("0x32c4ae2c1f1981195f9904466a39c9948fe30bbff2660be1715a4589334c74c7");
    var GY = BigInt("0xbc3736a2f4f6779c59bdcee36b692153d0a9877cc62a474002df32e52139f0a0");
    var IV = [
        0x7380166f, 0x4914b2b9, 0x172442d7, 0xda8a0600,
        0xa96f30bc, 0x163138aa, 0xe38dee4d, 0xb0fb0e4e
    ];
    var G = { x: GX, y: GY };

    function mod(value, m) {
        var result = value % m;
        return result >= 0n ? result : result + m;
    }

    function modPow(base, exp, m) {
        var result = 1n;
        base = mod(base, m);
        while (exp > 0n) {
            if (exp & 1n) result = mod(result * base, m);
            base = mod(base * base, m);
            exp >>= 1n;
        }
        return result;
    }

    function inverse(value) {
        return modPow(mod(value, P), P - 2n, P);
    }

    function isOnCurve(point) {
        if (!point) return true;
        return mod(point.y * point.y - point.x * point.x * point.x - A * point.x - B, P) === 0n;
    }

    function pointAdd(p, q) {
        if (!p) return q;
        if (!q) return p;
        if (p.x === q.x) {
            if (mod(p.y + q.y, P) === 0n) return null;
            return pointDouble(p);
        }
        var lambda = mod((q.y - p.y) * inverse(q.x - p.x), P);
        var x = mod(lambda * lambda - p.x - q.x, P);
        var y = mod(lambda * (p.x - x) - p.y, P);
        return { x: x, y: y };
    }

    function pointDouble(p) {
        if (!p || p.y === 0n) return null;
        var lambda = mod((3n * p.x * p.x + A) * inverse(2n * p.y), P);
        var x = mod(lambda * lambda - 2n * p.x, P);
        var y = mod(lambda * (p.x - x) - p.y, P);
        return { x: x, y: y };
    }

    function scalarMult(point, scalar) {
        var result = null;
        var addend = point;
        while (scalar > 0n) {
            if (scalar & 1n) result = pointAdd(result, addend);
            addend = pointDouble(addend);
            scalar >>= 1n;
        }
        return result;
    }

    function hexToBigInt(hex) {
        return BigInt("0x" + hex);
    }

    function bigIntToBytes(value, length) {
        var hex = value.toString(16);
        while (hex.length < length * 2) hex = "0" + hex;
        return hexToBytes(hex);
    }

    function hexToBytes(hex) {
        var clean = String(hex || "").replace(/^0x/i, "").replace(/\s+/g, "");
        if (clean.length % 2) clean = "0" + clean;
        var out = new Uint8Array(clean.length / 2);
        for (var i = 0; i < clean.length; i += 2) {
            out[i / 2] = parseInt(clean.slice(i, i + 2), 16);
        }
        return out;
    }

    function bytesToHex(bytes) {
        var out = "";
        for (var i = 0; i < bytes.length; i++) {
            out += bytes[i].toString(16).padStart(2, "0");
        }
        return out;
    }

    function concatBytes() {
        var length = 0;
        for (var i = 0; i < arguments.length; i++) length += arguments[i].length;
        var out = new Uint8Array(length);
        var offset = 0;
        for (var j = 0; j < arguments.length; j++) {
            out.set(arguments[j], offset);
            offset += arguments[j].length;
        }
        return out;
    }

    function utf8Bytes(text) {
        return new TextEncoder().encode(String(text));
    }

    function randomScalar() {
        var bytes = new Uint8Array(32);
        do {
            crypto.getRandomValues(bytes);
            var k = hexToBigInt(bytesToHex(bytes));
        } while (k === 0n || k >= N);
        return k;
    }

    function rotl(x, n) {
        return ((x << n) | (x >>> (32 - n))) >>> 0;
    }

    function p0(x) {
        return (x ^ rotl(x, 9) ^ rotl(x, 17)) >>> 0;
    }

    function p1(x) {
        return (x ^ rotl(x, 15) ^ rotl(x, 23)) >>> 0;
    }

    function sm3(data) {
        var msg = Array.prototype.slice.call(data);
        var bitLen = msg.length * 8;
        msg.push(0x80);
        while ((msg.length % 64) !== 56) msg.push(0);
        var high = Math.floor(bitLen / 0x100000000);
        var low = bitLen >>> 0;
        for (var i = 3; i >= 0; i--) msg.push((high >>> (i * 8)) & 0xff);
        for (var j = 3; j >= 0; j--) msg.push((low >>> (j * 8)) & 0xff);

        var v = IV.slice();
        var w = new Array(68);
        var wp = new Array(64);
        for (var offset = 0; offset < msg.length; offset += 64) {
            for (var t = 0; t < 16; t++) {
                var p = offset + t * 4;
                w[t] = ((msg[p] << 24) | (msg[p + 1] << 16) | (msg[p + 2] << 8) | msg[p + 3]) >>> 0;
            }
            for (var t2 = 16; t2 < 68; t2++) {
                w[t2] = (p1(w[t2 - 16] ^ w[t2 - 9] ^ rotl(w[t2 - 3], 15)) ^ rotl(w[t2 - 13], 7) ^ w[t2 - 6]) >>> 0;
            }
            for (var t3 = 0; t3 < 64; t3++) wp[t3] = (w[t3] ^ w[t3 + 4]) >>> 0;

            var a = v[0], b = v[1], c = v[2], d = v[3], e = v[4], f = v[5], g = v[6], h = v[7];
            for (var round = 0; round < 64; round++) {
                var tj = round < 16 ? 0x79cc4519 : 0x7a879d8a;
                var ss1 = rotl((((rotl(a, 12) + e) >>> 0) + rotl(tj, round % 32)) >>> 0, 7);
                var ss2 = (ss1 ^ rotl(a, 12)) >>> 0;
                var ff = round < 16 ? (a ^ b ^ c) : ((a & b) | (a & c) | (b & c));
                var gg = round < 16 ? (e ^ f ^ g) : ((e & f) | ((~e) & g));
                var tt1 = (((ff + d) >>> 0) + ss2 + wp[round]) >>> 0;
                var tt2 = (((gg + h) >>> 0) + ss1 + w[round]) >>> 0;
                d = c;
                c = rotl(b, 9);
                b = a;
                a = tt1;
                h = g;
                g = rotl(f, 19);
                f = e;
                e = p0(tt2);
            }
            v[0] ^= a; v[1] ^= b; v[2] ^= c; v[3] ^= d;
            v[4] ^= e; v[5] ^= f; v[6] ^= g; v[7] ^= h;
        }
        var out = new Uint8Array(32);
        for (var k = 0; k < 8; k++) {
            out[k * 4] = (v[k] >>> 24) & 0xff;
            out[k * 4 + 1] = (v[k] >>> 16) & 0xff;
            out[k * 4 + 2] = (v[k] >>> 8) & 0xff;
            out[k * 4 + 3] = v[k] & 0xff;
        }
        return out;
    }

    function kdf(z, length) {
        var out = new Uint8Array(length);
        var ct = 1;
        var offset = 0;
        while (offset < length) {
            var counter = new Uint8Array([
                (ct >>> 24) & 0xff, (ct >>> 16) & 0xff, (ct >>> 8) & 0xff, ct & 0xff
            ]);
            var digest = sm3(concatBytes(z, counter));
            out.set(digest.slice(0, Math.min(digest.length, length - offset)), offset);
            offset += digest.length;
            ct++;
        }
        return out;
    }

    function allZero(bytes) {
        for (var i = 0; i < bytes.length; i++) if (bytes[i] !== 0) return false;
        return true;
    }

    function toBase64(bytes) {
        var binary = "";
        for (var i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
        return btoa(binary);
    }

    function parsePublicKey(publicKeyHex) {
        var clean = String(publicKeyHex || "").replace(/^0x/i, "").replace(/\s+/g, "");
        if (clean.length !== 130 || clean.slice(0, 2) !== "04") {
            throw new Error("SM2 public key must be uncompressed hex");
        }
        var point = {
            x: hexToBigInt(clean.slice(2, 66)),
            y: hexToBigInt(clean.slice(66, 130))
        };
        if (!isOnCurve(point)) throw new Error("SM2 public key is not on curve");
        return point;
    }

    function encryptToBase64(publicKeyHex, plaintext) {
        if (!global.crypto || !global.crypto.getRandomValues) {
            throw new Error("browser crypto is unavailable");
        }
        var publicKey = parsePublicKey(publicKeyHex);
        var message = utf8Bytes(plaintext);
        if (!message.length) throw new Error("password is required");

        var c1, shared, z, t;
        do {
            var k = randomScalar();
            c1 = scalarMult(G, k);
            shared = scalarMult(publicKey, k);
            z = concatBytes(bigIntToBytes(shared.x, 32), bigIntToBytes(shared.y, 32));
            t = kdf(z, message.length);
        } while (allZero(t));

        var c2 = new Uint8Array(message.length);
        for (var i = 0; i < message.length; i++) c2[i] = message[i] ^ t[i];
        var c3 = sm3(concatBytes(bigIntToBytes(shared.x, 32), message, bigIntToBytes(shared.y, 32)));
        var c1Bytes = concatBytes(new Uint8Array([0x04]), bigIntToBytes(c1.x, 32), bigIntToBytes(c1.y, 32));
        return toBase64(concatBytes(c1Bytes, c3, c2));
    }

    global.NATTSM2 = {
        encryptToBase64: encryptToBase64,
        SM3: sm3
    };
})(window);
