/*{
  "DESCRIPTION": "Motion trails via a persistent feedback buffer; decay controls how long they linger",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Stylize"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "decay", "TYPE": "float", "DEFAULT": 0.85}
  ],
  "PASSES": [
    {"TARGET": "trail", "PERSISTENT": true},
    {}
  ]
}*/
void main() {
  vec2 uv = isf_FragNormCoord;
  if (PASSINDEX == 0) {
    vec4 cur = IMG_NORM_PIXEL(inputImage, uv);
    vec4 prev = IMG_NORM_PIXEL(trail, uv);
    gl_FragColor = max(cur, prev * clamp(decay, 0.0, 0.99));
  } else {
    gl_FragColor = IMG_NORM_PIXEL(trail, uv);
  }
}
