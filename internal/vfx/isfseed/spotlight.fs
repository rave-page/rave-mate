/*{
  "DESCRIPTION": "Darkens everything outside a circular spot; center is normalized 0..1",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Stylize"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "center", "TYPE": "point2D", "DEFAULT": [0.5, 0.5]},
    {"NAME": "radius", "TYPE": "float", "DEFAULT": 0.35},
    {"NAME": "darkness", "TYPE": "float", "DEFAULT": 0.85}
  ]
}*/
void main() {
  vec4 c = IMG_THIS_PIXEL(inputImage);
  vec2 p = isf_FragNormCoord - center;
  p.x *= RENDERSIZE.x / RENDERSIZE.y;
  float d = length(p);
  float m = smoothstep(radius, radius * 1.6 + 0.05, d);
  gl_FragColor = vec4(c.rgb * (1.0 - m * darkness), c.a);
}
