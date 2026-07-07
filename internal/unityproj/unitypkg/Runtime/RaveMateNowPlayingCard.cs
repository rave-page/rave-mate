// NOTE: not compiled/run by the Go generator - needs in-Unity verification.
// Now-playing card fed by a rave-mate gist (nowplaying.json): while a DJ is
// live, shows artist/track + their rave.page link and toggles liveRoot on/off.
// Track text comes through rave-mate's session output (redaction-filtered
// upstream). Image = build-time VRCUrl slot (Udon URLs are build-time only).
#if UDONSHARP
using UdonSharp;
using UnityEngine;
using UnityEngine.UI;
using VRC.SDK3.Data;
using VRC.SDK3.Image;
using VRC.SDK3.StringLoading;
using VRC.SDKBase;
using VRC.Udon.Common.Interfaces;

namespace RavePage.Mate.Runtime
{
    [UdonBehaviourSyncMode(BehaviourSyncMode.None)]
    public class RaveMateNowPlayingCard : UdonSharpBehaviour
    {
        [Tooltip("rave-mate nowplaying.json gist raw URL")] public VRCUrl sourceUrl;
        [Tooltip("Re-poll seconds (min 60; publisher writes ≤1/min + ~5 min CDN)")] public float refreshSeconds = 90f;
        [Tooltip("Enabled while live, disabled while idle")] public GameObject liveRoot;
        public Text trackText;
        public Text linkText;
        [Tooltip("Build-time card image (VRC image-allowlisted host)")] public VRCUrl imageUrl;
        public RawImage image;

        VRCImageDownloader _img;

        void Start()
        {
            if (liveRoot != null) liveRoot.SetActive(false);
            _Refresh();
            if (image != null && imageUrl != null && !string.IsNullOrEmpty(imageUrl.Get()))
            {
                _img = new VRCImageDownloader();
                _img.DownloadImage(imageUrl, null, (IUdonEventReceiver)this, null);
            }
        }

        public void _Refresh()
        {
            if (sourceUrl != null && !string.IsNullOrEmpty(sourceUrl.Get()))
                VRCStringDownloader.LoadUrl(sourceUrl, (IUdonEventReceiver)this);
            SendCustomEventDelayedSeconds(nameof(_Refresh), Mathf.Max(refreshSeconds, 60f));
        }

        public override void OnStringLoadSuccess(IVRCStringDownload result)
        {
            if (!VRCJson.TryDeserializeFromJson(result.Result, out DataToken root)) return;
            if (root.TokenType != TokenType.DataDictionary) return;
            DataDictionary d = root.DataDictionary;
            bool live = d.TryGetValue("live", out DataToken l) && l.TokenType == TokenType.Boolean && l.Boolean;
            if (liveRoot != null) liveRoot.SetActive(live);
            if (!live) return;
            string artist = str(d, "artist"), track = str(d, "track"), dj = str(d, "dj");
            if (trackText != null)
            {
                string line = artist == "" ? track : artist + " - " + track;
                if (dj != "") line = dj + (line == "" ? "" : ": " + line);
                trackText.text = line;
            }
            if (linkText != null) linkText.text = str(d, "link");
        }

        string str(DataDictionary d, string key)
        {
            if (d.TryGetValue(key, out DataToken t) && t.TokenType == TokenType.String) return t.String;
            return "";
        }

        public override void OnImageLoadSuccess(IVRCImageDownload result)
        {
            if (image != null) image.texture = result.Result;
        }
    }
}
#endif
