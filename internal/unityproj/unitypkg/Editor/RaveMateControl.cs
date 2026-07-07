// NOTE: not compiled/run by the Go generator - needs in-Unity verification.
// Local control socket so rave-mate (or a test agent) can drive the editor.
// Listens on 127.0.0.1:47625, line-based requests, one JSON line per reply.
// Socket threads MUST NOT touch the Unity API: work is marshalled onto the main
// thread via EditorApplication.update and a thread-safe queue.
using System;
using System.Collections.Generic;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using UnityEditor;
using UnityEngine;

namespace RavePage.Mate.Editor
{
    [InitializeOnLoad]
    public static class RaveMateControl
    {
        const int Port = 47625;
        const string MotionDir = "Assets/rave.page/Motion";

        static TcpListener _listener;
        static Thread _accept;
        static volatile bool _running;
        static readonly object _qlock = new object();
        static readonly Queue<Action> _main = new Queue<Action>();

        static RaveMateControl()
        {
            try
            {
                _listener = new TcpListener(IPAddress.Loopback, Port);
                _listener.Start();
                _running = true;
                _accept = new Thread(AcceptLoop) { IsBackground = true, Name = "RaveMateControl" };
                _accept.Start();
                EditorApplication.update += PumpMain;
                AppDomain.CurrentDomain.DomainUnload += (s, e) => Stop();
                EditorApplication.quitting += Stop;
            }
            catch (Exception e)
            {
                Debug.LogWarning("[rave.page] control socket not started: " + e.Message);
            }
        }

        static void Stop()
        {
            _running = false;
            try { _listener?.Stop(); } catch { /* shutting down */ }
            _listener = null;
        }

        // PumpMain drains queued main-thread actions (runs on EditorApplication.update).
        static void PumpMain()
        {
            for (;;)
            {
                Action a;
                lock (_qlock)
                {
                    if (_main.Count == 0) return;
                    a = _main.Dequeue();
                }
                try { a(); } catch (Exception e) { Debug.LogError("[rave.page] ctl: " + e); }
            }
        }

        // RunOnMain enqueues fn and blocks the socket thread until it completes.
        static string RunOnMain(Func<string> fn)
        {
            string result = null;
            Exception err = null;
            using (var done = new ManualResetEventSlim(false))
            {
                lock (_qlock)
                {
                    _main.Enqueue(() =>
                    {
                        try { result = fn(); }
                        catch (Exception e) { err = e; }
                        finally { done.Set(); }
                    });
                }
                if (!done.Wait(10000)) return "{\"ok\":false,\"error\":\"timeout\"}";
            }
            if (err != null) return "{\"ok\":false,\"error\":" + JsonStr(err.Message) + "}";
            return result;
        }

        static void AcceptLoop()
        {
            while (_running)
            {
                TcpClient client;
                try { client = _listener.AcceptTcpClient(); }
                catch { break; } // listener stopped
                ThreadPool.QueueUserWorkItem(_ => Handle(client));
            }
        }

        static void Handle(TcpClient client)
        {
            try
            {
                using (client)
                using (var ns = client.GetStream())
                using (var r = new StreamReader(ns, Encoding.UTF8))
                using (var w = new StreamWriter(ns, new UTF8Encoding(false)) { AutoFlush = true, NewLine = "\n" })
                {
                    string line;
                    while ((line = r.ReadLine()) != null)
                    {
                        var resp = Dispatch(line.Trim());
                        w.WriteLine(resp);
                        if (line.Trim().StartsWith("QUIT", StringComparison.OrdinalIgnoreCase)) break;
                    }
                }
            }
            catch (Exception e)
            {
                Debug.LogWarning("[rave.page] ctl client: " + e.Message);
            }
        }

        static string Dispatch(string req)
        {
            if (string.IsNullOrEmpty(req)) return "{\"ok\":false,\"error\":\"empty\"}";
            var sp = req.IndexOf(' ');
            var cmd = (sp < 0 ? req : req.Substring(0, sp)).ToUpperInvariant();
            var arg = sp < 0 ? "" : req.Substring(sp + 1).Trim();

            switch (cmd)
            {
                case "PING":
                    return "{\"ok\":true,\"app\":\"rave.page-unity\",\"ver\":\"0.1.0\"}";
                case "PERM-SOURCES":
                    return RunOnMain(PermSources);
                case "LIST-TAKES":
                    return RunOnMain(ListTakes);
                case "SCREENSHOT":
                    return RunOnMain(() => Screenshot(arg));
                case "QUIT":
                    return "{\"ok\":true}";
                default:
                    return "{\"ok\":false,\"error\":\"unknown command\"}";
            }
        }

        // PermSources returns the world-sync handoff file rave-mate wrote
        // (Assets/rave.page/WorldSync/sources.json) - see RaveMateWorldSyncWindow.
        static string PermSources()
        {
            const string p = "Assets/rave.page/WorldSync/sources.json";
            if (!File.Exists(p)) return "{\"ok\":false,\"error\":\"no sources.json - publish + write from rave-mate first\"}";
            return File.ReadAllText(p, Encoding.UTF8);
        }

        static string ListTakes()
        {
            var sb = new StringBuilder("[");
            if (Directory.Exists(MotionDir))
            {
                var files = Directory.GetFiles(MotionDir, "*.anim", SearchOption.AllDirectories);
                for (int i = 0; i < files.Length; i++)
                {
                    if (i > 0) sb.Append(',');
                    sb.Append(JsonStr(Path.GetFileNameWithoutExtension(files[i])));
                }
            }
            sb.Append(']');
            return sb.ToString();
        }

        // Screenshot writes a PNG of the active SceneView (or a temp preview camera) to path.
        static string Screenshot(string path)
        {
            if (string.IsNullOrEmpty(path)) return "{\"ok\":false,\"error\":\"path required\"}";
            var sv = SceneView.lastActiveSceneView;
            Camera cam = sv != null ? sv.camera : Camera.main;
            GameObject temp = null;
            if (cam == null)
            {
                temp = new GameObject("RaveMatePreviewCam");
                cam = temp.AddComponent<Camera>();
            }
            try
            {
                int w = 1280, h = 720;
                var rt = new RenderTexture(w, h, 24);
                var prev = cam.targetTexture;
                var prevActive = RenderTexture.active;
                cam.targetTexture = rt;
                cam.Render();
                RenderTexture.active = rt;
                var tex = new Texture2D(w, h, TextureFormat.RGB24, false);
                tex.ReadPixels(new Rect(0, 0, w, h), 0, 0);
                tex.Apply();
                cam.targetTexture = prev;
                RenderTexture.active = prevActive;
                var png = tex.EncodeToPNG();
                var dir = Path.GetDirectoryName(path);
                if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);
                File.WriteAllBytes(path, png);
                UnityEngine.Object.DestroyImmediate(tex);
                UnityEngine.Object.DestroyImmediate(rt);
                return "{\"ok\":true,\"path\":" + JsonStr(path) + "}";
            }
            finally
            {
                if (temp != null) UnityEngine.Object.DestroyImmediate(temp);
            }
        }

        // JsonStr minimally escapes s as a JSON string literal.
        static string JsonStr(string s)
        {
            if (s == null) return "\"\"";
            var sb = new StringBuilder(s.Length + 2);
            sb.Append('"');
            foreach (var c in s)
            {
                switch (c)
                {
                    case '"': sb.Append("\\\""); break;
                    case '\\': sb.Append("\\\\"); break;
                    case '\n': sb.Append("\\n"); break;
                    case '\r': sb.Append("\\r"); break;
                    case '\t': sb.Append("\\t"); break;
                    default:
                        if (c < 0x20) sb.Append("\\u").Append(((int)c).ToString("x4"));
                        else sb.Append(c);
                        break;
                }
            }
            sb.Append('"');
            return sb.ToString();
        }
    }
}
