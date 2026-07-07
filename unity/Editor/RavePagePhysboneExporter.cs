// rave.page physbone exporter - Unity Editor menu item that exports the selected
// avatar's FBX + a `<name>.physbones.json` sidecar for rave-mate's motion studio
// (internal/vrmdyn consumes it: real PhysBone/DynamicBone params instead of name
// heuristics). Drop this file into Assets/Editor/. No hard dependencies: VRCPhysBone,
// DynamicBone and the FBX Exporter package are all resolved via reflection.
//
// Sidecar format = vrmdyn.Sidecar (version 1), see rave-mate internal/vrmdyn.

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text;
using UnityEditor;
using UnityEngine;

namespace RavePage
{
    public static class PhysboneExporter
    {
        const string MenuPath = "rave.page/Export Avatar for rave-mate…";

        [MenuItem(MenuPath, true)]
        static bool Validate() => Selection.activeGameObject != null;

        [MenuItem(MenuPath)]
        static void Export()
        {
            var root = Selection.activeGameObject;
            var dir = EditorUtility.OpenFolderPanel("Export avatar for rave-mate", "", "");
            if (string.IsNullOrEmpty(dir)) return;

            var baseName = Sanitize(root.name);
            var fbxOut = Path.Combine(dir, baseName + ".fbx");
            var sidecarOut = Path.Combine(dir, baseName + ".physbones.json");

            var fbxHow = ExportFbx(root, fbxOut);
            var chains = CollectPhysBones(root);
            var source = "vrcphysbone";
            if (chains.Count == 0)
            {
                chains = CollectDynamicBones(root);
                source = "dynamicbone";
            }
            File.WriteAllText(sidecarOut, ToJson(source, chains), new UTF8Encoding(false));

            Debug.Log($"[rave.page] exported {baseName}: fbx={fbxHow}, {chains.Count} physbone chain(s) → {sidecarOut}");
            EditorUtility.RevealInFinder(sidecarOut);
            if (chains.Count == 0)
                Debug.LogWarning("[rave.page] no VRCPhysBone / DynamicBone components found under the selection - rave-mate falls back to name heuristics.");
        }

        // ── FBX ──

        // Prefer the FBX Exporter package (exports the avatar exactly as configured);
        // fall back to copying the source .fbx asset the mesh came from.
        static string ExportFbx(GameObject root, string outPath)
        {
            var exporter = Type.GetType("UnityEditor.Formats.Fbx.Exporter.ModelExporter, Unity.Formats.Fbx.Editor");
            var export = exporter?.GetMethod("ExportObject", BindingFlags.Public | BindingFlags.Static,
                null, new[] { typeof(string), typeof(UnityEngine.Object) }, null);
            if (export != null)
            {
                export.Invoke(null, new object[] { outPath, root });
                return "fbx-exporter";
            }

            var smr = root.GetComponentInChildren<SkinnedMeshRenderer>();
            var mesh = smr != null ? smr.sharedMesh : null;
            var src = mesh != null ? AssetDatabase.GetAssetPath(mesh) : null;
            if (!string.IsNullOrEmpty(src) && src.EndsWith(".fbx", StringComparison.OrdinalIgnoreCase))
            {
                File.Copy(src, outPath, true);
                return "copied source asset (" + Path.GetFileName(src) + ")";
            }
            Debug.LogWarning("[rave.page] FBX not exported: install the 'FBX Exporter' package (com.unity.formats.fbx) or use an avatar whose mesh asset is an .fbx. Sidecar written anyway.");
            return "SKIPPED";
        }

        // ── chains ──

        class Chain
        {
            public string Root;
            public List<string> Ignore = new List<string>();
            public double Pull, Spring, Stiffness, Gravity, GravityFalloff, Immobile, Radius;
            public Vector3 Endpoint;
        }

        static List<Chain> CollectPhysBones(GameObject root)
        {
            var list = new List<Chain>();
            foreach (var c in root.GetComponentsInChildren<Component>(true))
            {
                if (c == null || c.GetType().Name != "VRCPhysBone") continue;
                var t = c.GetType();
                var rootTf = Get<Transform>(t, c, "rootTransform") ?? c.transform;
                var ch = new Chain
                {
                    Root = rootTf.name,
                    Pull = GetF(t, c, "pull"),
                    Spring = GetF(t, c, "spring"),
                    Stiffness = GetF(t, c, "stiffness"),
                    Gravity = GetF(t, c, "gravity"),
                    GravityFalloff = GetF(t, c, "gravityFalloff"),
                    Immobile = GetF(t, c, "immobile"),
                    Radius = GetF(t, c, "radius"),
                    Endpoint = Get<Vector3?>(t, c, "endpointPosition") ?? Vector3.zero,
                };
                var ignores = Get<object>(t, c, "ignoreTransforms") as System.Collections.IEnumerable;
                if (ignores != null)
                    foreach (var it in ignores)
                        if (it is Transform tf && tf != null) ch.Ignore.Add(tf.name);
                list.Add(ch);
            }
            return list;
        }

        static List<Chain> CollectDynamicBones(GameObject root)
        {
            var list = new List<Chain>();
            foreach (var c in root.GetComponentsInChildren<Component>(true))
            {
                if (c == null || c.GetType().Name != "DynamicBone") continue;
                var t = c.GetType();
                var rootTf = Get<Transform>(t, c, "m_Root") ?? c.transform;
                var grav = Get<Vector3?>(t, c, "m_Gravity") ?? Vector3.zero;
                var ch = new Chain
                {
                    Root = rootTf.name,
                    // DynamicBone → physbone-semantics mapping (vrmdyn maps onward to verlet):
                    Pull = GetF(t, c, "m_Elasticity"),
                    Spring = 1.0 - GetF(t, c, "m_Damping"),
                    Stiffness = GetF(t, c, "m_Stiffness"),
                    Gravity = -grav.y, // vrmdyn gravity: y-down positive
                    Radius = GetF(t, c, "m_Radius"),
                    Endpoint = Get<Vector3?>(t, c, "m_EndOffset") ?? Vector3.zero,
                };
                var ex = Get<object>(t, c, "m_Exclusions") as System.Collections.IEnumerable;
                if (ex != null)
                    foreach (var it in ex)
                        if (it is Transform tf && tf != null) ch.Ignore.Add(tf.name);
                list.Add(ch);
            }
            return list;
        }

        // ── reflection + json helpers ──

        static T Get<T>(Type t, object o, string field)
        {
            var f = t.GetField(field, BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Instance);
            if (f == null) return default;
            var v = f.GetValue(o);
            return v is T tv ? tv : default;
        }

        static double GetF(Type t, object o, string field)
        {
            var f = t.GetField(field, BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Instance);
            var v = f?.GetValue(o);
            return v is float fl ? fl : v is double d ? d : 0;
        }

        // Minimal writer - avoids JsonUtility (can't do lists of plain objects cleanly).
        static string ToJson(string source, List<Chain> chains)
        {
            var sb = new StringBuilder();
            sb.Append("{\n  \"version\": 1,\n  \"source\": \"").Append(source).Append("\",\n  \"chains\": [");
            for (int i = 0; i < chains.Count; i++)
            {
                var c = chains[i];
                sb.Append(i > 0 ? "," : "").Append("\n    {");
                sb.Append("\"root\": ").Append(Str(c.Root));
                sb.Append(", \"ignore\": [").Append(string.Join(", ", c.Ignore.Select(Str))).Append(']');
                sb.Append(", \"pull\": ").Append(Num(c.Pull));
                sb.Append(", \"spring\": ").Append(Num(c.Spring));
                sb.Append(", \"stiffness\": ").Append(Num(c.Stiffness));
                sb.Append(", \"gravity\": ").Append(Num(c.Gravity));
                sb.Append(", \"gravityFalloff\": ").Append(Num(c.GravityFalloff));
                sb.Append(", \"immobile\": ").Append(Num(c.Immobile));
                sb.Append(", \"endpointPosition\": [").Append(Num(c.Endpoint.x)).Append(", ").Append(Num(c.Endpoint.y)).Append(", ").Append(Num(c.Endpoint.z)).Append(']');
                sb.Append(", \"radius\": ").Append(Num(c.Radius));
                sb.Append('}');
            }
            sb.Append("\n  ]\n}\n");
            return sb.ToString();
        }

        static string Str(string s) =>
            "\"" + (s ?? "").Replace("\\", "\\\\").Replace("\"", "\\\"") + "\"";

        static string Num(double d) =>
            d.ToString("0.####", System.Globalization.CultureInfo.InvariantCulture);

        static string Sanitize(string s) =>
            string.Concat(s.Select(ch => Path.GetInvalidFileNameChars().Contains(ch) ? '_' : ch)).Trim();
    }
}
