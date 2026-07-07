// NOTE: not compiled/run by the Go generator - needs in-Unity verification.
// World Sync window: lists the gist source URLs rave-mate wrote to
// Assets/rave.page/WorldSync/sources.json, copies them, and wires them into
// scene components: our own UdonSharp readers (sourceUrl) or a VideoTXL
// Remote Whitelist (best-effort by type/property name - VideoTXL is not a
// compile-time dependency).
using System;
using System.Collections.Generic;
using System.IO;
using UnityEditor;
using UnityEngine;

namespace RavePage.Mate.Editor
{
    public class RaveMateWorldSyncWindow : EditorWindow
    {
        const string SourcesPath = "Assets/rave.page/WorldSync/sources.json";

        [Serializable] class Source { public string kind; public string name; public string url; public string jsonUrl; }
        [Serializable] class SourceDoc { public List<Source> sources = new List<Source>(); }

        SourceDoc _doc = new SourceDoc();
        Vector2 _scroll;
        string _status = "";

        [MenuItem("Tools/rave.page/World Sync")]
        public static void Open()
        {
            var w = GetWindow<RaveMateWorldSyncWindow>("rave.page World Sync");
            w.Reload();
            w.Show();
        }

        void Reload()
        {
            _doc = new SourceDoc();
            _status = "";
            if (!File.Exists(SourcesPath))
            {
                _status = "No sources.json - publish in rave-mate, then Worlds → Unity projects → Write source URLs.";
                return;
            }
            try { _doc = JsonUtility.FromJson<SourceDoc>(File.ReadAllText(SourcesPath)) ?? new SourceDoc(); }
            catch (Exception e) { _status = "sources.json unreadable: " + e.Message; }
        }

        void OnGUI()
        {
            if (GUILayout.Button("Reload")) Reload();
            if (!string.IsNullOrEmpty(_status)) EditorGUILayout.HelpBox(_status, MessageType.Info);

            _scroll = EditorGUILayout.BeginScrollView(_scroll);
            foreach (var s in _doc.sources)
            {
                EditorGUILayout.BeginVertical("box");
                var title = string.IsNullOrEmpty(s.name) ? s.kind : s.kind + ": " + s.name;
                EditorGUILayout.LabelField(title, EditorStyles.boldLabel);
                EditorGUILayout.LabelField(s.url, EditorStyles.miniLabel);
                EditorGUILayout.BeginHorizontal();
                if (GUILayout.Button("Copy URL"))
                    EditorGUIUtility.systemCopyBuffer = s.url;
                if (!string.IsNullOrEmpty(s.jsonUrl) && GUILayout.Button("Copy JSON URL"))
                    EditorGUIUtility.systemCopyBuffer = s.jsonUrl;
                if (GUILayout.Button("Wire into selection"))
                    WireSelection(s);
                EditorGUILayout.EndHorizontal();
                EditorGUILayout.EndVertical();
            }
            EditorGUILayout.EndScrollView();

            EditorGUILayout.HelpBox(
                "Wire into selection sets the selected component's remote-URL field:\n" +
                "• rave.page reader prefabs (PosterBoard/EventsBoard/NowPlayingCard): sourceUrl\n" +
                "• VideoTXL Remote Whitelist: its Remote String URL (perm lists - newline or JSON mode)\n" +
                "Images cannot be gist-driven: VRCUrl is build-time only - pre-wire image slots on the prefab.",
                MessageType.None);
        }

        // WireSelection sets a VRCUrl-typed serialized property (child "url" string) on the
        // selected GameObject's components. Ours match by field name "sourceUrl"; VideoTXL by
        // type name containing "RemoteWhitelist" + a property name containing "url".
        void WireSelection(Source s)
        {
            var go = Selection.activeGameObject;
            if (go == null) { _status = "Select a GameObject first."; return; }
            foreach (var comp in go.GetComponents<Component>())
            {
                if (comp == null) continue;
                var typeName = comp.GetType().Name;
                var so = new SerializedObject(comp);
                var it = so.GetIterator();
                bool wired = false;
                while (it.NextVisible(true))
                {
                    // VRCUrl serializes as a container with a string child "url".
                    var isUrlish = it.propertyType == SerializedPropertyType.Generic &&
                                   it.name.IndexOf("url", StringComparison.OrdinalIgnoreCase) >= 0;
                    if (!isUrlish) continue;
                    var ours = it.name == "sourceUrl";
                    var txl = typeName.IndexOf("RemoteWhitelist", StringComparison.OrdinalIgnoreCase) >= 0;
                    if (!ours && !txl) continue;
                    var inner = it.FindPropertyRelative("url");
                    if (inner == null || inner.propertyType != SerializedPropertyType.String) continue;
                    inner.stringValue = s.url;
                    so.ApplyModifiedProperties();
                    wired = true;
                    _status = "Wired " + s.kind + " → " + typeName + "." + it.name;
                    Debug.Log("[rave.page] " + _status);
                    break;
                }
                if (wired) return;
            }
            _status = "No wireable URL field found on selection - use Copy URL instead.";
        }
    }
}
