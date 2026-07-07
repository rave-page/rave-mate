// NOTE: not compiled/run by the Go generator - needs in-Unity verification.
// Upcoming-events board fed by a rave-mate gist (events.json): renders
// "title - date" lines into a Text. Wire sourceUrl via Tools → rave.page →
// World Sync.
#if UDONSHARP
using UdonSharp;
using UnityEngine;
using UnityEngine.UI;
using VRC.SDK3.Data;
using VRC.SDK3.StringLoading;
using VRC.SDKBase;
using VRC.Udon.Common.Interfaces;

namespace RavePage.Mate.Runtime
{
    [UdonBehaviourSyncMode(BehaviourSyncMode.None)]
    public class RaveMateEventsBoard : UdonSharpBehaviour
    {
        [Tooltip("rave-mate events.json gist raw URL")] public VRCUrl sourceUrl;
        [Tooltip("Re-poll seconds (min 60)")] public float refreshSeconds = 600f;
        [Tooltip("Max rows rendered")] public int maxEvents = 8;
        public Text target;
        [Tooltip("Shown when no events")] public string emptyText = "No upcoming events";

        void Start() { _Refresh(); }

        public void _Refresh()
        {
            if (sourceUrl != null && !string.IsNullOrEmpty(sourceUrl.Get()))
                VRCStringDownloader.LoadUrl(sourceUrl, (IUdonEventReceiver)this);
            SendCustomEventDelayedSeconds(nameof(_Refresh), Mathf.Max(refreshSeconds, 60f));
        }

        public override void OnStringLoadSuccess(IVRCStringDownload result)
        {
            if (target == null) return;
            if (!VRCJson.TryDeserializeFromJson(result.Result, out DataToken root)) return;
            if (root.TokenType != TokenType.DataDictionary) return;
            if (!root.DataDictionary.TryGetValue("events", out DataToken events)) return;
            if (events.TokenType != TokenType.DataList) return;
            DataList list = events.DataList;
            string outText = "";
            int n = 0;
            for (int i = 0; i < list.Count && n < maxEvents; i++)
            {
                DataToken e = list[i];
                if (e.TokenType != TokenType.DataDictionary) continue;
                string title = "", date = "";
                if (e.DataDictionary.TryGetValue("title", out DataToken t) && t.TokenType == TokenType.String) title = t.String;
                if (e.DataDictionary.TryGetValue("date", out DataToken d) && d.TokenType == TokenType.String) date = d.String;
                if (title == "") continue;
                if (outText != "") outText += "\n";
                outText += date == "" ? title : title + " - " + date;
                n++;
            }
            target.text = outText == "" ? emptyText : outText;
        }
    }
}
#endif
