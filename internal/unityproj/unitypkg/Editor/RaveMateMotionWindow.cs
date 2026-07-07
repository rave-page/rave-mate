// NOTE: not compiled/run by the Go generator - needs in-Unity verification.
// EditorWindow for rave.page motion takes: list .anim clips, preview on the real
// scene avatar via AnimationMode, build an AnimatorController, export to VRM.
using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using UnityEditor;
using UnityEditor.Animations;
using UnityEngine;
using UnityEngine.SceneManagement;

namespace RavePage.Mate.Editor
{
    public class RaveMateMotionWindow : EditorWindow
    {
        const string MotionDir = "Assets/rave.page/Motion";

        readonly List<string> _clipPaths = new List<string>();
        Vector2 _scroll;
        int _selected = -1;
        GameObject _avatar;          // scene avatar to preview onto
        bool _avatarAutoPicked;      // true = _avatar came from auto-pick, safe to replace
        AnimationClip _clip;         // currently selected clip
        float _time;                 // preview time (seconds)
        bool _playing;
        double _lastUpdate;
        double _lastScan;            // last Motion-dir poll time
        string _dirSig;              // signature of last poll (paths + mtimes)

        [MenuItem("Tools/rave.page/Motion")]
        public static void Open()
        {
            var w = GetWindow<RaveMateMotionWindow>("rave.page Motion");
            w.minSize = new Vector2(360, 420);
            w.Refresh();
        }

        void OnEnable()
        {
            Refresh();
            AutoPickAvatar();
            _dirSig = DirSignature(); // seed so first poll only fires on a real change
            EditorApplication.update += OnUpdate;
        }

        // Unity message: hierarchy/scene changed - re-resolve the avatar if empty or stale.
        void OnHierarchyChange()
        {
            AutoPickAvatar();
        }

        void OnDisable()
        {
            EditorApplication.update -= OnUpdate;
            StopPreview();
        }

        // Refresh rescans the Motion dir for .anim clips.
        void Refresh()
        {
            _clipPaths.Clear();
            if (Directory.Exists(MotionDir))
            {
                foreach (var p in Directory.GetFiles(MotionDir, "*.anim", SearchOption.AllDirectories))
                    _clipPaths.Add(p.Replace('\\', '/'));
            }
            if (_selected >= _clipPaths.Count) _selected = _clipPaths.Count - 1;
        }

        // PollMotionDir auto-refreshes when rave-mate drops/updates a .anim (throttled ~1s).
        // Polling (vs FileSystemWatcher) runs on the main thread - no delayCall marshaling -
        // and tolerates MotionDir not existing until the first export.
        void PollMotionDir()
        {
            double now = EditorApplication.timeSinceStartup;
            if (now - _lastScan < 1.0) return;
            _lastScan = now;
            var sig = DirSignature();
            if (sig == _dirSig) return;
            _dirSig = sig;
            AssetDatabase.Refresh(); // import newly-dropped clips so LoadAssetAtPath resolves
            Refresh();
            Repaint();
        }

        // DirSignature = joined paths + last-write ticks; changes when a .anim is added/edited.
        string DirSignature()
        {
            if (!Directory.Exists(MotionDir)) return "";
            return string.Join(";", Directory
                .GetFiles(MotionDir, "*.anim", SearchOption.AllDirectories)
                .Select(p => p + ":" + File.GetLastWriteTimeUtc(p).Ticks));
        }

        void OnGUI()
        {
            EditorGUILayout.LabelField("Recorded Motion Takes", EditorStyles.boldLabel);
            using (new EditorGUILayout.HorizontalScope())
            {
                EditorGUILayout.LabelField(MotionDir, EditorStyles.miniLabel);
                if (GUILayout.Button("Refresh", GUILayout.Width(70))) Refresh();
            }

            if (_clipPaths.Count == 0)
            {
                EditorGUILayout.HelpBox(
                    "No .anim takes found. Export a take from rave-mate; it lands under " + MotionDir + ".",
                    MessageType.Info);
            }
            else
            {
                _scroll = EditorGUILayout.BeginScrollView(_scroll, GUILayout.MaxHeight(140));
                for (int i = 0; i < _clipPaths.Count; i++)
                {
                    bool sel = i == _selected;
                    bool now = GUILayout.Toggle(sel, Path.GetFileNameWithoutExtension(_clipPaths[i]), "Button");
                    if (now && !sel) Select(i);
                }
                EditorGUILayout.EndScrollView();
            }

            EditorGUILayout.Space();
            var picked = (GameObject)EditorGUILayout.ObjectField(
                new GUIContent("Avatar (scene)"), _avatar, typeof(GameObject), true);
            if (picked != _avatar)
            {
                _avatar = picked;
                _avatarAutoPicked = false; // user's explicit choice (or explicit clear)
            }

            DrawPreview();

            EditorGUILayout.Space();
            using (new EditorGUI.DisabledScope(_clip == null || _avatar == null))
            {
                if (GUILayout.Button("Add to avatar (AnimatorController)")) AddToAvatar();
            }
            EditorGUILayout.HelpBox(
                "Add to avatar creates/extends an AnimatorController beside the avatar with the " +
                "selected clip as a state. For VRChat expression-menu toggles, use VRCFury instead.",
                MessageType.None);

            EditorGUILayout.Space();
            using (new EditorGUI.DisabledScope(_avatar == null))
            {
                if (GUILayout.Button("Export avatar as VRM")) ExportVrm();
            }
        }

        void DrawPreview()
        {
            using (new EditorGUI.DisabledScope(_clip == null || _avatar == null))
            {
                using (new EditorGUILayout.HorizontalScope())
                {
                    bool play = GUILayout.Toggle(_playing, _playing ? "Pause" : "Play", "Button", GUILayout.Width(70));
                    if (play != _playing) TogglePlay(play);
                    float len = _clip != null ? _clip.length : 1f;
                    float t = EditorGUILayout.Slider(_time, 0f, Mathf.Max(0.0001f, len));
                    if (!Mathf.Approximately(t, _time))
                    {
                        _time = t;
                        Sample();
                    }
                }
            }
        }

        void Select(int i)
        {
            _selected = i;
            _clip = AssetDatabase.LoadAssetAtPath<AnimationClip>(_clipPaths[i]);
            _time = 0f;
            if (_avatar != null) Sample();
        }

        void TogglePlay(bool play)
        {
            _playing = play;
            _lastUpdate = EditorApplication.timeSinceStartup;
            if (play) StartPreview();
        }

        void StartPreview()
        {
            if (!AnimationMode.InAnimationMode()) AnimationMode.StartAnimationMode();
        }

        void StopPreview()
        {
            _playing = false;
            if (AnimationMode.InAnimationMode()) AnimationMode.StopAnimationMode();
        }

        // Sample drives the clip onto the avatar at the current time via AnimationMode.
        void Sample()
        {
            if (_clip == null || _avatar == null) return;
            StartPreview();
            AnimationMode.BeginSampling();
            AnimationMode.SampleAnimationClip(_avatar, _clip, _time);
            AnimationMode.EndSampling();
            SceneView.RepaintAll();
        }

        void OnUpdate()
        {
            PollMotionDir();
            if (!_playing || _clip == null || _avatar == null) return;
            double now = EditorApplication.timeSinceStartup;
            _time += (float)(now - _lastUpdate);
            _lastUpdate = now;
            if (_time >= _clip.length) _time = 0f; // loop
            Sample();
            Repaint();
        }

        // AutoPickAvatar fills the avatar field from the active scene when it is empty or its
        // object is gone/inactive. Never replaces a live manual pick.
        void AutoPickAvatar()
        {
            if (_avatar != null && _avatar.activeInHierarchy) return;      // current pick still good
            if (_avatar != null && !_avatarAutoPicked) return;             // manual pick alive → respect
            string reason;
            var cand = FindAvatarCandidate(out reason);
            if (cand == null || cand == _avatar) return;
            _avatar = cand;
            _avatarAutoPicked = true;
            Debug.Log("[rave.page] auto-picked avatar: " + cand.name + " (" + reason + ")");
            Repaint();
        }

        // FindAvatarCandidate ranks scene avatars: VRCAvatarDescriptor (reflection, no VRCSDK dep)
        // over humanoid Animator; active-in-hierarchy over inactive; EditorOnly-tagged skipped.
        static GameObject FindAvatarCandidate(out string reason)
        {
            reason = null;
            var scene = SceneManager.GetActiveScene();
            if (!scene.IsValid()) return null;
            var descType = FindType("VRC.SDK3.Avatars.Components.VRCAvatarDescriptor");

            GameObject vrcActive = null, vrcAny = null, humanActive = null, humanAny = null;
            foreach (var root in scene.GetRootGameObjects())
            {
                if (descType != null)
                {
                    foreach (Component c in root.GetComponentsInChildren(descType, true))
                    {
                        var go = c.gameObject;
                        if (go.CompareTag("EditorOnly")) continue;
                        if (go.activeInHierarchy) { if (vrcActive == null) vrcActive = go; }
                        else if (vrcAny == null) vrcAny = go;
                    }
                }
                foreach (var a in root.GetComponentsInChildren<Animator>(true))
                {
                    if (!a.isHuman) continue;
                    var go = a.gameObject;
                    if (go.CompareTag("EditorOnly")) continue;
                    if (go.activeInHierarchy) { if (humanActive == null) humanActive = go; }
                    else if (humanAny == null) humanAny = go;
                }
            }

            if (vrcActive != null) { reason = "VRCAvatarDescriptor, active"; return vrcActive; }
            if (vrcAny != null) { reason = "VRCAvatarDescriptor, inactive"; return vrcAny; }
            if (humanActive != null) { reason = "humanoid Animator, active"; return humanActive; }
            if (humanAny != null) { reason = "humanoid Animator, inactive"; return humanAny; }
            return null;
        }

        // AddToAvatar creates/extends an AnimatorController beside the avatar with the clip.
        void AddToAvatar()
        {
            if (_clip == null || _avatar == null) return;
            var animator = _avatar.GetComponent<Animator>();
            if (animator == null) animator = _avatar.AddComponent<Animator>();

            var ctrlPath = MotionDir + "/" + _avatar.name + "_rave.controller";
            var ctrl = AssetDatabase.LoadAssetAtPath<AnimatorController>(ctrlPath);
            if (ctrl == null)
            {
                Directory.CreateDirectory(MotionDir);
                ctrl = AnimatorController.CreateAnimatorControllerAtPath(ctrlPath);
            }

            var sm = ctrl.layers[0].stateMachine;
            var name = _clip.name;
            AnimatorState existing = null;
            foreach (var c in sm.states)
                if (c.state.name == name) { existing = c.state; break; }
            if (existing == null)
            {
                var st = sm.AddState(name);
                st.motion = _clip;
            }
            else
            {
                existing.motion = _clip;
            }

            animator.runtimeAnimatorController = ctrl;
            EditorUtility.SetDirty(ctrl);
            AssetDatabase.SaveAssets();
            EditorUtility.DisplayDialog("rave.page Motion",
                "Added '" + name + "' to " + ctrlPath, "OK");
        }

        // ExportVrm opens UniVRM's exporter: known window types via reflection, then the export
        // menu items by path (ExecuteMenuItem). Logs detected UniVRM assemblies for remote debug.
        void ExportVrm()
        {
            if (_avatar == null) AutoPickAvatar(); // same resolution when nothing selected
            if (_avatar == null) return;
            Selection.activeGameObject = _avatar; // wizard + menu routes both read the selection

            var vrmAsms = VrmAssemblies();
            string detected = DescribeUniVrm(vrmAsms);
            Debug.Log("[rave.page] UniVRM detection: " + detected);

            if (vrmAsms.Length == 0)
            {
                EditorUtility.DisplayDialog("UniVRM not installed",
                    "VRM export needs UniVRM. Install it via the Unity Package Manager " +
                    "(add package from git URL https://github.com/vrm-c/UniVRM) then retry.",
                    "OK");
                return;
            }

            if (OpenExporterWindow()) return;

            var menuPaths = ExportMenuPaths();
            foreach (var p in menuPaths)
            {
                if (!EditorApplication.ExecuteMenuItem(p)) continue;
                Debug.Log("[rave.page] VRM export menu opened: " + p);
                return;
            }

            EditorUtility.DisplayDialog("rave.page Motion",
                "UniVRM is installed (" + detected + ") but no exporter entry point worked.\n\n" +
                "Menu paths tried:\n  " + string.Join("\n  ", menuPaths.ToArray()) + "\n\n" +
                "The avatar is already selected - open the VRM0 / VRM1 export menu manually.",
                "OK");
        }

        // Exporter window types across UniVRM lineages, 0.x first; searched in ALL loaded
        // assemblies (assembly names moved across versions, so no assembly-qualified lookup).
        static readonly string[] ExporterTypeNames =
        {
            "VRM.VRMExporterWizard",      // 0.x >=0.66, EditorWindow
            "VRM.VRMExportationWizard",   // 0.x legacy ScriptableWizard
            "VRM.VRMExporterMenu",        // 0.x menu shim
            "UniVRM10.VRM10ExportDialog", // VRM-1.0
        };

        static readonly string[] ExporterEntryNames = { "Open", "CreateWizard", "OpenExportMenu", "ExportFromMenu" };

        // OpenExporterWindow reflects a known exporter type + static entry point; false → try menus.
        bool OpenExporterWindow()
        {
            foreach (var typeName in ExporterTypeNames)
            {
                var t = FindType(typeName);
                if (t == null) continue;
                foreach (var entry in ExporterEntryNames)
                {
                    MethodInfo m;
                    try { m = t.GetMethod(entry, BindingFlags.Public | BindingFlags.Static); }
                    catch (AmbiguousMatchException) { continue; }
                    if (m == null) continue;
                    var ps = m.GetParameters();
                    if (ps.Length > 1) continue;
                    if (ps.Length == 1 && !ps[0].ParameterType.IsAssignableFrom(typeof(GameObject))) continue;
                    try
                    {
                        m.Invoke(null, ps.Length == 0 ? null : new object[] { _avatar });
                        Debug.Log("[rave.page] VRM exporter opened via " + typeName + "." + entry);
                        return true;
                    }
                    catch (Exception e)
                    {
                        Debug.LogWarning("[rave.page] " + typeName + "." + entry + " threw: " + e.Message);
                    }
                }
            }
            return false;
        }

        // ExportMenuPaths = known export menu paths across UniVRM versions (0.x before 1.0),
        // plus the version-suffixed legacy 0.x path built from VRM.VRMVersion.MENU.
        static List<string> ExportMenuPaths()
        {
            var paths = new List<string>
            {
                "VRM0/Export to VRM 0.x",
                "VRM/Export to VRM 0.x",
                "VRM1/Export VRM-1.0",
                "VRM/Export VRM-1.0",
            };
            var menu = ConstString("VRM.VRMVersion", "MENU"); // "UniVRM-0.XX.X" or "VRM/UniVRM-0.XX.X"
            if (!string.IsNullOrEmpty(menu))
            {
                paths.Insert(2, menu + "/Export humanoid");
                if (menu.IndexOf('/') < 0) paths.Insert(2, "VRM/" + menu + "/Export humanoid");
            }
            return paths;
        }

        // VrmAssemblies = loaded assemblies belonging to UniVRM (VRM*, VRM10*, UniVRM*, UniGLTF*).
        static Assembly[] VrmAssemblies()
        {
            return AppDomain.CurrentDomain.GetAssemblies().Where(a =>
            {
                var n = a.GetName().Name;
                return n.StartsWith("VRM", StringComparison.Ordinal)
                    || n.StartsWith("UniVRM", StringComparison.Ordinal)
                    || n.StartsWith("UniGLTF", StringComparison.Ordinal);
            }).ToArray();
        }

        // DescribeUniVrm = variant + version + assembly names, for the dialog and Debug.Log.
        static string DescribeUniVrm(Assembly[] asms)
        {
            if (asms.Length == 0) return "no UniVRM assemblies loaded";
            bool v0 = asms.Any(a => a.GetName().Name == "VRM" || a.GetName().Name == "VRM.Editor");
            bool v1 = asms.Any(a => a.GetName().Name.StartsWith("VRM10", StringComparison.Ordinal));
            string variant = v0 && v1 ? "VRM 0.x + 1.0" : v0 ? "VRM 0.x" : v1 ? "VRM 1.0" : "unknown variant";
            string ver = ConstString("UniGLTF.UniGLTFVersion", "VERSION");
            var names = string.Join(", ", asms.Select(a => a.GetName().Name).ToArray());
            return variant + (ver != null ? " v" + ver : "") + " [" + names + "]";
        }

        // FindType scans all loaded assemblies (UniVRM types moved between assemblies over time).
        static Type FindType(string fullName)
        {
            foreach (var a in AppDomain.CurrentDomain.GetAssemblies())
            {
                var t = a.GetType(fullName, false);
                if (t != null) return t;
            }
            return null;
        }

        // ConstString reads a public const/static string field via reflection; null if absent.
        static string ConstString(string typeName, string field)
        {
            var t = FindType(typeName);
            var f = t != null ? t.GetField(field, BindingFlags.Public | BindingFlags.Static) : null;
            return f != null ? f.GetValue(null) as string : null;
        }
    }
}
